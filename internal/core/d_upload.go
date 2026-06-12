// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"sort"
	"time"

	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

type uploadReq struct {
	peer *Peer
	req  proto.ChunkRequest
}

var errUploadPaused = errors.New("upload paused")

const (
	uploadSlots        = 4 // default number of upload slots
	optimisticUnchokeN = 1 // number of optimistic unchoke slots
	unchokeInterval    = 10 * time.Second
)

// unchokeLoop periodically selects which peers get upload slots.
// Based on libtorrent\'s choking algorithm:
// - Sort interested peers by upload rate (descending)
// - Unchoke top N peers
// - Reserve 1 slot for optimistic unchoke (round-robin).
func (d *Download) unchokeLoop() {
	timer := time.NewTimer(unchokeInterval)
	defer timer.Stop()

	optimisticIdx := 0

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
			timer.Reset(unchokeInterval)

			if d.GetState()&(Downloading|Seeding) == 0 {
				continue
			}

			d.recalculateUnchokeSlots(&optimisticIdx)
		}
	}
}

func (d *Download) recalculateUnchokeSlots(optimisticIdx *int) {
	type peerRate struct {
		peer *Peer
		rate int64
	}

	var candidates []peerRate
	var unchokable []*Peer

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.closed.Load() {
			return true
		}
		if p.peerInterested.Load() {
			candidates = append(candidates, peerRate{
				peer: p,
				rate: p.ioOut.Status().CurRate,
			})
		}
		if !p.peerInterested.Load() || p.snubbed.Load() {
			// choke peers that are not interested or snubbed
			if !p.ourChoking.CompareAndSwap(false, true) {
				go p.sendEventX(Event{Event: proto.Choke})
			}
		}
		return true
	})

	// sort by upload rate descending (fastest first)
	slices.SortFunc(candidates, func(a, b peerRate) int {
		if a.rate > b.rate {
			return -1
		}
		if a.rate < b.rate {
			return 1
		}
		return 0
	})

	// unchoke top-N peers by rate
	normalSlots := uploadSlots - optimisticUnchokeN
	for i, c := range candidates {
		if i >= normalSlots {
			break
		}
		unchokable = append(unchokable, c.peer)
	}

	// optimistic unchoke: pick the peer that has been waiting longest
	if len(candidates) > normalSlots {
		// rotate through candidates beyond the normal slots
		if *optimisticIdx >= len(candidates)-normalSlots {
			*optimisticIdx = 0
		}
		if normalSlots+*optimisticIdx < len(candidates) {
			unchokable = append(unchokable, candidates[normalSlots+*optimisticIdx].peer)
			*optimisticIdx++
		}
	}

	// apply: unchoke selected peers, choke the rest
	for _, p := range unchokable {
		if p.ourChoking.CompareAndSwap(true, false) {
			go p.sendEventX(Event{Event: proto.Unchoke})
		}
	}

	// trigger response scheduling for newly unchoked peers
	select {
	case d.scheduleResponseSignal <- empty.Empty{}:
	default:
	}
}

// backgroundReqHandler processes upload requests from peers.
// It collects requests from unchoked peers, groups them by piece,
// caches piece data to avoid redundant disk reads, and dispatches
// to the upload pool for bounded concurrent processing.
func (d *Download) backgroundReqHandler() {
	var requestsByPiece = make(map[uint32][]uploadReq, 64)
	var pieceOrder []struct {
		index uint32
		count int
	}

	const maxResponsesPerWake = 1024
	const maxRequestsToConsiderPerWake = maxResponsesPerWake * 8

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleResponseSignal:
			if !d.HasState(Downloading | Seeding) {
				continue
			}

			pieceOrder = pieceOrder[:0]
			clear(requestsByPiece)

			considered := 0
			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if considered >= maxRequestsToConsiderPerWake {
					return false
				}

				if p.closed.Load() || p.ourChoking.Load() {
					return true
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

			sort.SliceStable(pieceOrder, func(i, j int) bool {
				if pieceOrder[i].count == pieceOrder[j].count {
					return pieceOrder[i].index < pieceOrder[j].index
				}
				return pieceOrder[i].count > pieceOrder[j].count
			})

			responses := 0
		pieceLoop:
			for _, po := range pieceOrder {
				reqs := requestsByPiece[po.index]
				if len(reqs) > 1 {
					if err := d.dispatchCachedPiece(po.index, reqs, &responses, maxResponsesPerWake); err != nil {
						d.setError(err)
					}
				} else {
					for _, item := range reqs {
						if !d.dispatchUpload(item, &responses, maxResponsesPerWake) {
							continue pieceLoop
						}
					}
				}
				if responses >= maxResponsesPerWake {
					break
				}
			}
		}
	}
}

func (d *Download) dispatchUpload(item uploadReq, responses *int, maxResponses int) bool {
	if *responses >= maxResponses {
		select {
		case d.scheduleResponseSignal <- empty.Empty{}:
		default:
		}
		return false
	}

	if !d.c.tryEnqueueUpload(uploadTask{d: d, peer: item.peer, req: item.req}) {
		select {
		case d.scheduleResponseSignal <- empty.Empty{}:
		default:
		}
		*responses = maxResponses
		return false
	}

	*responses++
	return true
}

func (d *Download) dispatchCachedPiece(index uint32, reqs []uploadReq, responses *int, maxResponses int) error {
	buf := mempool.GetWithCap(int(d.pieceLength(index)))
	defer mempool.Put(buf)

	if err := d.readPiece(index, buf.B); err != nil {
		return err
	}

	for _, item := range reqs {
		data := make([]byte, item.req.Length)
		copy(data, buf.B[item.req.Begin:item.req.Begin+item.req.Length])
		if !d.dispatchUploadWithData(item, data, responses, maxResponses) {
			break
		}
	}
	return nil
}

func (d *Download) dispatchUploadWithData(item uploadReq, data []byte, responses *int, maxResponses int) bool {
	if *responses >= maxResponses {
		select {
		case d.scheduleResponseSignal <- empty.Empty{}:
		default:
		}
		return false
	}

	if !d.c.tryEnqueueUpload(uploadTask{d: d, peer: item.peer, req: item.req, data: data}) {
		select {
		case d.scheduleResponseSignal <- empty.Empty{}:
		default:
		}
		*responses = maxResponses
		return false
	}

	*responses++
	return true
}

// buf must be big enough to read whole piece.
func (d *Download) readPiece(index uint32, buf []byte) error {
	var offset int64 = 0
	for _, chunk := range d.pieceInfo.fileChunks(index) {
		f, err := d.openFileReadOnly(chunk.fileIndex)
		if err != nil {
			return err
		}

		n, err := f.File.ReadAt(buf[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			if int64(n) != chunk.length || err != io.EOF {
				f.Release()
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
}

// readPieceRangeCtx reads a range of bytes from a piece into dst,
// checking for context cancellation and download state between chunks.
func (d *Download) readPieceRangeCtx(ctx context.Context, req proto.ChunkRequest, dst []byte) error {
	if int(req.Length) != len(dst) {
		return fmt.Errorf("invalid dst length: req=%d dst=%d", req.Length, len(dst))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !d.HasState(Downloading | Seeding) {
		return errUploadPaused
	}

	start := int64(req.PieceIndex)*d.info.PieceLength + int64(req.Begin)
	end := start + int64(req.Length)

	var offset int64
	for _, chunk := range fileChunks(d.info, start, end) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.HasState(Downloading | Seeding) {
			return errUploadPaused
		}

		f, err := d.openFileReadOnly(chunk.fileIndex)
		if err != nil {
			return err
		}

		n, err := f.File.ReadAt(dst[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			if int64(n) != chunk.length || err != io.EOF {
				f.Release()
				return err
			}
		}

		offset += chunk.length
		f.Release()
	}

	return nil
}
