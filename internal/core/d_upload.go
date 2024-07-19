// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"io"
	"net/netip"
	"time"

	"github.com/sourcegraph/conc"

	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/heap"
	"tyr/internal/pkg/mempool"
	"tyr/internal/proto"
)

func (d *Download) backgroundReqHandler() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleResponse:
			if d.GetState()|Downloading|Seeding == 0 {
				continue
			}

			clear(d.reqPieceCount)

			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if p.peerChoking.CompareAndSwap(true, false) {
					p.Unchoke()
				} else {
					return true
				}

				if p.Bitmap.Count() == d.info.NumPieces {
					return true
				}

				if !p.peerInterested.Load() {
					return true
				}

				p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
					d.reqPieceCount[key.PieceIndex]++
					return true
				})

				return true
			})

			var s = make([]pieceRare, 0, len(d.reqPieceCount))

			for index, rare := range d.reqPieceCount {
				s = append(s, pieceRare{
					index: index,
					rare:  rare,
				})
			}

			if len(s) == 0 {
				continue
			}

			h := heap.FromSlice(s)

			pieceReq := h.Pop()

			buf := mempool.GetWithCap(int(d.pieceLength(pieceReq.index)))
			//piece := make([]byte, d.pieceLength(pieceIndex))
			err := d.readPiece(pieceReq.index, buf.B)
			if err != nil {
				mempool.Put(buf)
				d.setError(err)
				continue
			}

			var g conc.WaitGroup

			d.log.Debug().Msgf("upload piece %d", pieceReq.index)

			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if !p.peerInterested.Load() {
					return true
				}

				p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
					if key.PieceIndex == pieceReq.index {
						d.ioUp.Update(int(key.Length))
						d.c.ioUp.Update(int(key.Length))
						d.uploaded.Add(int64(key.Length))
						g.Go(func() {
							p.Response(&proto.ChunkResponse{
								Data:       buf.B[key.Begin : key.Begin+key.Length],
								Begin:      key.Begin,
								PieceIndex: pieceReq.index,
							})
						})
					}
					return true
				})
				return true
			})

			g.Wait()
			mempool.Put(buf)

			time.Sleep(time.Second)
		}
	}
}

// buf must be bigger enough to read whole piece
func (d *Download) readPiece(index uint32, buf []byte) error {
	pieces := d.pieceInfo[index]
	var offset int64 = 0
	for _, chunk := range pieces.fileChunks {
		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			return err
		}

		n, err := f.File.ReadAt(buf[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			if !(int64(n) == chunk.length && err == io.EOF) {
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
}
