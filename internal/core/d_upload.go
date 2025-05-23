// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"io"
	"net/netip"

	"github.com/sourcegraph/conc"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

type cacheKey struct {
	hash  metainfo.Hash
	index uint32
}

func (d *Download) backgroundReqHandler() {
	var reqPieceCount = make(map[uint32]uint32, d.info.NumPieces)
	var peers []*Peer

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleResponseSignal:
			if !d.wait(Downloading | Seeding) {
				continue
			}

			clear(reqPieceCount)
			clear(peers)
			peers = peers[:0]

			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if p.peerInterested.Load() {
					if p.ourChoking.CompareAndSwap(true, false) {
						p.Unchoke()
					}
				}

				if p.peerRequests.Size() != 0 {
					peers = append(peers, p)
				}
				return true
			})

			var s = make([]pieceRare, 0, len(reqPieceCount))

			for _, peer := range peers {
				peer.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
					reqPieceCount[key.PieceIndex]++
					return true
				})
			}

			for index, rare := range reqPieceCount {
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

			var buf *mempool.Buffer

			buf = mempool.GetWithCap(int(d.pieceLength(pieceReq.index)))

			err := d.readPiece(pieceReq.index, buf.B)
			if err != nil {
				mempool.Put(buf)
				d.setError(err)
				continue
			}

			var g conc.WaitGroup

			for _, p := range peers {
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
			}

			g.Wait()
		}
	}
}

// buf must be bigger enough to read whole piece.
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
			if int64(n) != chunk.length || err != io.EOF {
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
}
