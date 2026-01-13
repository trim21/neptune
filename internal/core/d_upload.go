// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"

	"neptune/internal/pkg/empty"
	"neptune/internal/proto"
)

type uploadReq struct {
	peer *Peer
	req  proto.ChunkRequest
}

var errUploadPaused = errors.New("upload paused")

func (d *Download) backgroundReqHandler() {
	var requestsByPiece = make(map[uint32][]uploadReq, 64)
	var pieceOrder []struct {
		index uint32
		count int
	}

	const maxResponsesPerWake = 1024
	// Hard cap the amount of requests we inspect per wake.
	// Without this, a request storm (many peers * deep request queues) would make us
	// spend a long time ranging maps and sorting, even though we only send a bounded
	// number of responses.
	const maxRequestsToConsiderPerWake = maxResponsesPerWake * 8

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleResponseSignal:
			if !d.wait(Downloading | Seeding) {
				continue
			}

			pieceOrder = pieceOrder[:0]
			clear(requestsByPiece)

			considered := 0
			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if p.peerInterested.Load() {
					if p.ourChoking.CompareAndSwap(true, false) {
						p.Unchoke()
					}
				}

				if considered >= maxRequestsToConsiderPerWake {
					return false
				}

				if p.peerRequests.Size() == 0 {
					return true
				}

				p.peerRequests.Range(func(key proto.ChunkRequest, _ empty.Empty) bool {
					requestsByPiece[key.PieceIndex] = append(requestsByPiece[key.PieceIndex], uploadReq{peer: p, req: key})
					considered++
					return considered < maxRequestsToConsiderPerWake
				})

				return considered < maxRequestsToConsiderPerWake
			})

			if len(requestsByPiece) == 0 {
				continue
			}

			for pieceIndex, reqs := range requestsByPiece {
				pieceOrder = append(pieceOrder, struct {
					index uint32
					count int
				}{index: pieceIndex, count: len(reqs)})
			}

			sort.Slice(pieceOrder, func(i, j int) bool {
				if pieceOrder[i].count == pieceOrder[j].count {
					return pieceOrder[i].index < pieceOrder[j].index
				}
				return pieceOrder[i].count > pieceOrder[j].count
			})

			responses := 0
			for _, po := range pieceOrder {
				reqs := requestsByPiece[po.index]
				for _, item := range reqs {
					if responses >= maxResponsesPerWake {
						select {
						case d.scheduleResponseSignal <- empty.Empty{}:
						default:
						}
						break
					}

					if !d.c.tryEnqueueUpload(uploadTask{d: d, peer: item.peer, req: item.req}) {
						select {
						case d.scheduleResponseSignal <- empty.Empty{}:
						default:
						}
						responses = maxResponsesPerWake
						break
					}

					responses++
				}

				if responses >= maxResponsesPerWake {
					break
				}
			}
		}
	}
}

func (d *Download) readPieceRangeCtx(ctx context.Context, req proto.ChunkRequest, dst []byte) error {
	if int(req.Length) != len(dst) {
		return fmt.Errorf("invalid dst length: req=%d dst=%d", req.Length, len(dst))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d.GetState()&(Downloading|Seeding) == 0 {
		return errUploadPaused
	}

	start := int64(req.PieceIndex)*d.info.PieceLength + int64(req.Begin)
	end := start + int64(req.Length)

	var offset int64
	for _, chunk := range fileChunks(d.info, start, end) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.GetState()&(Downloading|Seeding) == 0 {
			return errUploadPaused
		}

		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			return err
		}

		n, err := f.File.ReadAt(dst[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			f.Release()
			if int64(n) != chunk.length || err != io.EOF {
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
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
