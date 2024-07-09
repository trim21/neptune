// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/netip"
	"time"

	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/gslice"
	"tyr/internal/proto"
)

func (d *Download) backgroundReqHandler() {
	// TODO: perf for cross seeding
	var pieceCount = make([]uint16, d.info.NumPieces)

	for {
		select {
		case <-d.ctx.Done():
			return
		default:
		}
		d.wait(Downloading | Seeding)

		clear(pieceCount)

		d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
			if p.peerChoking.Load() {
				p.peerChoking.Store(false)
				p.Unchoke()
			}

			if !p.peerInterested.Load() {
				return true
			}

			p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
				pieceCount[key.PieceIndex]++
				return true
			})
			return true
		})

		idx, v := gslice.Max(pieceCount)

		// no pending myRequests
		if v == 0 {
			continue
		}

		pieceIndex := uint32(idx)

		piece, err := d.readPiece(pieceIndex)
		if err != nil {
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
					d.uploaded.Add(int64(key.Length))
					go p.Response(proto.ChunkResponse{
						Data:       piece[key.Begin : key.Begin+key.Length],
						Begin:      key.Begin,
						PieceIndex: pieceIndex,
					})
				}
				return true
			})
			return true
		})

		time.Sleep(time.Second / 10)
	}
}

func (d *Download) readPiece(index uint32) ([]byte, error) {
	pieces := d.pieceInfo[index]
	buf := make([]byte, d.pieceLength(index))

	var offset int64 = 0
	for _, chunk := range pieces.fileChunks {
		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			return nil, err
		}

		_, err = f.File.ReadAt(buf[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			f.Close()
			return nil, err
		}

		offset += chunk.length
		f.Release()
	}

	return buf, nil
}
