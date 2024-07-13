// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"crypto/sha1"
	"net/netip"
	"slices"
	"time"

	"github.com/trim21/errgo"

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
		Uint32("piece", res.PieceIndex).
		Uint32("offset", res.Begin).
		Int("length", len(res.Data)).
		Msg("res received")

	d.ioDown.Update(len(res.Data))
	d.netDown.Update(len(res.Data))
	d.downloaded.Add(int64(len(res.Data)))

	pi := res.Begin / defaultBlockSize
	normalChunkLen := as.Uint32((d.info.PieceLength + defaultBlockSize - 1) / defaultBlockSize)
	cid := normalChunkLen*res.PieceIndex + pi

	pieceCidStart := res.PieceIndex * normalChunkLen
	pieceCidEnd := pieceCidStart + as.Uint32(pieceChunkLen(d.info, res.PieceIndex))

	//tasks.Submit(func() {
	err := d.writeChunkToDist(res)
	if err != nil {
		return
	}

	d.chunkMap.Add(cid)

	var pieceDone = true
	for i := pieceCidStart; i <= pieceCidEnd; i++ {
		if !d.chunkMap.Contains(i) {
			pieceDone = false
			break
		}
	}

	if !pieceDone {
		return
	}

	err = d.checkPiece(res.PieceIndex)
	if err != nil {
		return
	}

	d.checkDone()
}

func (d *Download) writeChunkToDist(res proto.ChunkResponse) error {
	size := int64(len(res.Data))
	begin := int64(res.Begin) + d.info.PieceLength*int64(res.PieceIndex)

	var offset int64
	for _, chunk := range fileChunks(d.info, begin, begin+size) {
		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			d.setError(err)
			return errgo.Wrap(err, "failed to open file for writing chunk")
		}

		_, err = f.File.WriteAt(res.Data[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			f.Close()
			d.setError(err)
			return errgo.Wrap(err, "failed to write chunk")
		}

		f.Release()
		offset += chunk.length
	}

	return nil
}

func (d *Download) checkPiece(pieceIndex uint32) error {
	size := d.pieceLength(pieceIndex)
	buf := mempool.GetWithCap(int(size))
	defer mempool.Put(buf)

	err := d.readPiece(pieceIndex, buf.B)
	if err != nil {
		d.setError(err)
		return errgo.Wrap(err, "failed to read piece")
	}

	if sha1.Sum(buf.B) != d.info.Pieces[pieceIndex] {
		d.corrupted.Add(d.info.PieceLength)
		return nil
	}

	d.bm.Set(pieceIndex)
	d.log.Trace().Msgf("piece %d done", pieceIndex)
	d.have(pieceIndex)

	return nil
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

func (d *Download) checkDone() {
	if d.bm.Count() != d.info.NumPieces {
		return
	}

	d.m.Lock()
	d.state = Seeding
	d.ioDown.Reset()
	d.m.Unlock()

	d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.Bitmap.Count() == d.info.NumPieces {
			p.cancel()
		}

		return true
	})
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
