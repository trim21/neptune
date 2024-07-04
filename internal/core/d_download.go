package core

import (
	"crypto/sha1"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/docker/go-units"

	"tyr/internal/meta"
	"tyr/internal/pkg/as"
	"tyr/internal/pkg/empty"
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
	d.log.Trace().
		Any("res", map[string]any{
			"piece":  res.PieceIndex,
			"offset": res.Begin,
			"length": len(res.Data),
		}).Msg("res received")

	d.ioDown.Update(len(res.Data))
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
			defer f.Release()

			_, err = f.File.WriteAt(buf.B[offset:offset+chunk.length], chunk.offsetOfFile)
			if err != nil {
				d.setError(err)
				return
			}

			offset += chunk.length
		}

		d.bm.Set(pieceIndex)

		d.log.Trace().Msgf("buf %d done", pieceIndex)
		d.have(pieceIndex)

		if d.bm.Count() == d.info.NumPieces {
			d.m.Lock()
			d.state = Uploading
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

func (d *Download) backgroundReqHandle() {
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
			time.Sleep(time.Second / 10)

			d.m.Lock()
			if d.state != Downloading {
				if d.state == Uploading {
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

			//d.log.Debug().Msg("backgroundReqHandle")

			//weight := avaPool.Get()

			//if cap(weight) < int(d.numPieces) {
			//}
			//var h heap.Interface[pair.Pair[uint32, uint32]]
			//weight := make([]pair.Pair[uint32, uint32], 0, int(d.numPieces))

			if d.seq.Load() {
				d.scheduleSeq()
				continue
			}

			h := make(PriorityQueue, d.info.NumPieces)

			for i := range h {
				h[i].Index = uint32(i)
			}

			d.conn.Range(func(key netip.AddrPort, p *Peer) bool {
				if p.Bitmap.Count() == 0 {
					return true
				}

				p.Bitmap.Range(func(i uint32) {
					h[i].Weight++
				})

				return true
			})

			sort.Sort(&h)

			for i, priority := range h {
				if i > 5 {
					break
				}

				fmt.Println(priority.Index, priority.Weight)
			}
		}
		//fmt.Println("index", v.Index)
		//avaPool.Put(weight)
	}
}

func (d *Download) scheduleSeq() {
	var found int64 = 0

	for index := uint32(0); index < d.info.NumPieces; index++ {
		if d.bm.Get(index) {
			continue
		}

		chunkLen := pieceChunkLen(d.info, index)

		d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
			if !p.Bitmap.Get(index) {
				return true
			}

			if p.requests.Size() >= int(p.QueueLimit.Load())/2 {
				return true
			}

			// TODO: handle reject
			//for _, chunk := range chunks {
			//	if _, rejected := p.Rejected.Load(chunk); rejected {
			//		return true
			//	}
			//}

			for i := 0; i < chunkLen; i++ {
				req := pieceChunk(d.info, index, i)
				_, exist := p.requestHistory.LoadOrStore(req, empty.Empty{})
				if exist {
					continue
				}

				p.Request(req)
			}

			if p.Rejected.Size() != 0 {
				fmt.Println(p.Rejected)
			}

			found++

			return false
		})

		if found >= 5 {
			break
		}

		if found*d.info.PieceLength >= units.GiB*2 {
			break
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

func pieceChunks(info meta.Info, index uint32) []proto.ChunkRequest {
	var numPerPiece = (info.PieceLength + defaultBlockSize - 1) / defaultBlockSize
	var rr = make([]proto.ChunkRequest, 0, numPerPiece)

	pieceStart := int64(index) * info.PieceLength

	pieceLen := min(info.PieceLength, info.TotalLength-pieceStart)

	for n := int64(0); n < numPerPiece; n++ {
		begin := defaultBlockSize * int64(n)
		length := uint32(min(pieceLen-begin, defaultBlockSize))

		if length <= 0 {
			break
		}

		rr = append(rr, proto.ChunkRequest{
			PieceIndex: index,
			Begin:      uint32(begin),
			Length:     length,
		})
	}

	return rr
}
