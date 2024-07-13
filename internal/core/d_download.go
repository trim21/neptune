// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"crypto/sha1"
	"net/netip"
	"slices"
	"time"

	"tyr/internal/meta"
	"tyr/internal/pkg/as"
	"tyr/internal/pkg/global/tasks"
	"tyr/internal/pkg/mempool"
	"tyr/internal/proto"
)

type downloadReq struct {
	conn *Peer
	r    proto.ChunkRequest
}

func (d *Download) have(index uint32) {
	tasks.Submit(func() {
		d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
			p.Have(index)
			return true
		})
	})
}

func (d *Download) handleRes(res proto.ChunkResponse) {
	// TODO: flush chunks to disk instead of waiting whole piece

	d.log.Trace().
		Any("res", map[string]any{
			"piece":  res.PieceIndex,
			"offset": res.Begin,
			"length": len(res.Data),
		}).Msg("res received")

	d.ioDown.Update(len(res.Data))
	d.netDown.Update(len(res.Data))
	d.downloaded.Add(int64(len(res.Data)))

	if d.bm.Get(res.PieceIndex) {
		return
	}

	d.pdMutex.Lock()
	defer d.pdMutex.Unlock()

	chunks, ok := d.pieceData[res.PieceIndex]
	if !ok {
		chunks = make([]*proto.ChunkResponse, pieceChunkLen(d.info, res.PieceIndex))
	}

	chunks[res.Begin/defaultBlockSize] = &res

	filled := true
	for _, res := range chunks {
		if res == nil {
			filled = false
			break
		}
	}

	if filled {
		delete(d.pieceData, res.PieceIndex)
		d.writePieceToDisk(res.PieceIndex, chunks)
		return
	}

	d.pieceData[res.PieceIndex] = chunks
}

func (d *Download) writePieceToDisk(pieceIndex uint32, chunks []*proto.ChunkResponse) {
	buf := mempool.Get()

	for i, chunk := range chunks {
		buf.Write(chunk.Data)
		chunks[i] = nil
	}

	// it's expected users have a hardware run sha1 faster than disk write

	h := sha1.Sum(buf.B)
	if h != d.info.Pieces[pieceIndex] {
		d.corrupted.Add(d.info.PieceLength)
		mempool.Put(buf)
		return
	}

	d.bm.Set(pieceIndex)

	tasks.Submit(func() {
		defer mempool.Put(buf)

		pieces := d.pieceInfo[pieceIndex]
		var offset int64 = 0

		for _, chunk := range pieces.fileChunks {
			f, err := d.openFile(chunk.fileIndex)
			if err != nil {
				d.setError(err)
				return
			}

			_, err = f.File.WriteAt(buf.B[offset:offset+chunk.length], chunk.offsetOfFile)
			if err != nil {
				f.Close()
				d.setError(err)
				return
			}

			f.Release()
			offset += chunk.length
		}

		d.bm.Set(pieceIndex)

		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)

		if d.bm.Count() == d.info.NumPieces {
			d.m.Lock()
			d.state = Seeding
			d.ioDown.Reset()
			d.m.Unlock()

			d.pdMutex.Lock()
			clear(d.pieceData)
			d.pdMutex.Unlock()

			d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
				if p.Bitmap.Count() == d.info.NumPieces {
					p.cancel()
				}

				return true
			})
		}
	})
}

func (d *Download) backgroundReqScheduler() {
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
			time.Sleep(time.Second / 10)

			d.m.Lock()
			if d.state != Downloading {
				if d.state == Seeding {
					d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
						if p.Bitmap.Count() == d.info.NumPieces {
							p.cancel()
						}
						return true
					})
				}
				d.cond.Wait()
			}
			d.m.Unlock()

			d.scheduleSeq()
		}
	}
}

func (d *Download) updateReqQueue() {
	if time.Since(d.reqLastUpdate) < time.Second*30 {
		return
	}
	d.reqLastUpdate = time.Now()

	var lastAdd uint32
	for len(d.reqQuery) < 5 {
		for index := lastAdd; index < d.info.NumPieces; index++ {
			if d.bm.Get(index) {
				continue
			}

			d.reqQuery = append(d.reqQuery, index)
			lastAdd = index + 1
			break
		}
	}
}

func (d *Download) scheduleSeq() {
	d.updateReqQueue()

	var peers = make([]*Peer, 0, d.conn.Size())

	d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
		peers = append(peers, p)
		return true
	})

	slices.SortFunc(peers, func(a, b *Peer) int {
		a.rttMutex.RLock()
		da := a.rttAverage.Average()
		a.rttMutex.RUnlock()

		b.rttMutex.RLock()
		db := b.rttAverage.Average()
		b.rttMutex.RUnlock()

		if da == db {
			return 0
		}

		// 0 means no data, so we order average RTT with 0 > 1m > 1ms
		if db == 0 {
			return -1
		}
		if da == 0 {
			return 1
		}
		if da < db {
			return -1
		}
		if da > db {
			return 1
		}

		return 0
	})

	for index := uint32(0); index < d.info.NumPieces; index++ {
		if d.bm.Get(index) {
			continue
		}

		chunkLen := pieceChunkLen(d.info, index)

		d.reqShedMutex.Lock()
		h, ok := d.reqHistory[index]
		d.reqShedMutex.Unlock()

		if ok {
			if _, pending := d.conn.Load(h); pending {
				continue
			}
		}

		for _, p := range peers {
			if !p.Bitmap.Get(index) {
				continue
			}

			if p.myRequests.Size()+chunkLen >= int(p.QueueLimit.Load()) {
				continue
			}

			for i := 0; i < chunkLen; i++ {
				req := pieceChunk(d.info, index, i)
				if _, rejected := p.Rejected.Load(req); rejected {
					continue
				}
				p.Request(req)
			}

			d.reqShedMutex.Lock()
			d.reqHistory[index] = p.Address
			d.reqShedMutex.Unlock()
		}
	}
}

func pieceChunkLen(info meta.Info, index uint32) int {
	pieceSize := info.PieceLength
	if index == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}

	return as.Int((pieceSize + defaultBlockSize - 1) / defaultBlockSize)
}

func pieceChunk(info meta.Info, index uint32, chunkIndex int) proto.ChunkRequest {
	pieceSize := info.PieceLength
	if index == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}

	begin := defaultBlockSize * int64(chunkIndex)
	end := min(begin+defaultBlockSize, pieceSize)

	return proto.ChunkRequest{
		PieceIndex: index,
		Begin:      uint32(begin),
		Length:     as.Uint32(end - begin),
	}
}
