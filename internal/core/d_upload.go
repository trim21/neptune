// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"io"
	"net/netip"
	"time"

	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/gslice"
	"tyr/internal/pkg/mempool"
	"tyr/internal/proto"
)

func (d *Download) backgroundReqHandler() {
	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}
		d.wait(Downloading | Seeding)

		clear(d.pieceCount)

		d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
			if p.peerChoking.Load() {
				p.peerChoking.Store(false)
				p.Unchoke()
			}

			if !p.peerInterested.Load() {
				return true
			}

			p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
				d.pieceCount[key.PieceIndex]++
				return true
			})
			return true
		})

		idx, v := gslice.Max(d.pieceCount)

		// no pending myRequests
		if v == 0 {
			continue
		}

		pieceIndex := uint32(idx)

		buf := mempool.GetWithCap(int(d.pieceLength(pieceIndex)))
		//piece := make([]byte, d.pieceLength(pieceIndex))
		err := d.readPiece(pieceIndex, buf.B)
		if err != nil {
			mempool.Put(buf)
			d.setError(err)
			continue
		}

		d.log.Debug().Msgf("upload piece %d", pieceIndex)

		d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
			if !p.peerInterested.Load() {
				return true
			}

			p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
				if key.PieceIndex == pieceIndex {
					d.ioUp.Update(int(key.Length))
					d.c.ioUp.Update(int(key.Length))
					d.uploaded.Add(int64(key.Length))
					go p.Response(proto.ChunkResponse{
						Data:       buf.B[key.Begin : key.Begin+key.Length],
						Begin:      key.Begin,
						PieceIndex: pieceIndex,
					})
				}
				return true
			})
			return true
		})
		mempool.Put(buf)

		time.Sleep(time.Second / 10)
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
				f.Close()
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
}
