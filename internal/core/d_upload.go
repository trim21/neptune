// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
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
	uploadSlots          = 4 // default number of upload slots
	optimisticUnchokeN   = 1 // reserved for optimistic unchoke
	unchokeInterval      = 10 * time.Second
	unchokeGracePeriod   = 50 * time.Second // don't choke peers recently unchoked
	unchokeCycleFraction = 8                // cycle ~1/8 = ~12.5% of slots per round
)

// peerUploadScore computes a weighted score for unchoke priority.
// Aligns with libtorrent's calculate_unchoke_upload_leech_experimental:
//   - Peers that have unchoked us (reciprocating) get order_base + rate bonus
//   - Peers that haven't unchoked us get random weight (optimistic queue)
//   - Preferred peers get a multiplier
func peerUploadScore(p *Peer) (score uint32, isReciprocal bool) {
	const orderBase uint32 = 1 << 30

	multiplier := uint32(1)
	if p.preferred.Load() {
		multiplier = 4
	}

	// Peer has unchoked us — they're reciprocating.
	if !p.peerChoking.Load() {
		downRate := uint32(p.pieceDownloadRate.Status().CurRate) / 64
		score = orderBase + downRate*multiplier
		return score, true
	}

	// Peer hasn't unchoked us — optimistic, random weight.
	// Lower weight for stingy peers (those we upload to at < 1KB/s).
	upRate := uint32(p.pieceUploadRate.Status().CurRate) / 64
	if upRate < 2048/64 {
		score = upRate * multiplier
	} else {
		base := uint32(1 << 10)
		if p.preferred.Load() {
			base *= 4
		}
		score = rand.Uint32N(base) * multiplier
	}
	return score, false
}

// recalculateUnchokeSlots re-evaluates which peers should be unchoked.
//
// Strategy (aligned with libtorrent):
//  1. Always choke snubbed and uninterested peers.
//  2. Sort candidates by weighted score (prefer reciprocating peers).
//  3. Unchoke top-N candidates, respecting grace period for recently unchoked.
//  4. Reserve one slot for optimistic unchoke (randomized, cycling).
//  5. Rotate a fraction of slots each round to discover faster peers.
func (d *Download) recalculateUnchokeSlots() {
	now := time.Now()

	type candidate struct {
		peer        *Peer
		score       uint32
		reciprocal  bool // peer has unchoked us
		recentlyUnc bool // unchoked within grace period
	}

	var candidates []candidate

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.closed.Load() {
			return true
		}

		// Always choke snubbed peers.
		if p.snubbed.Load() {
			if p.ourChoking.CompareAndSwap(false, true) {
				go p.sendEventX(Event{Event: proto.Choke})
			}
			return true
		}

		// Always choke uninterested peers — libtorrent only queues peers
		// that explicitly signaled Interested.
		if !p.peerInterested.Load() {
			if p.ourChoking.CompareAndSwap(false, true) {
				go p.sendEventX(Event{Event: proto.Choke})
			}
			return true
		}

		score, reciprocal := peerUploadScore(p)
		recently := now.Sub(p.lastUnchokeAt.Load()) < unchokeGracePeriod

		candidates = append(candidates, candidate{
			peer:        p,
			score:       score,
			reciprocal:  reciprocal,
			recentlyUnc: recently,
		})
		return true
	})

	if len(candidates) == 0 {
		return
	}

	// Sort by:
	// 1. Recently unchoked peers first (grace period protection)
	// 2. Reciprocal peers first (they're giving us data)
	// 3. Score descending
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.recentlyUnc != b.recentlyUnc {
			return a.recentlyUnc
		}
		if a.reciprocal != b.reciprocal {
			return a.reciprocal
		}
		return a.score > b.score
	})

	normalSlots := max(uploadSlots-optimisticUnchokeN, 1)

	// ── Normal slots (rate-based) ──────────────────────────────────
	// Rotate cycleSlotsToRotate slots each round to discover faster peers.
	totalSlots := min(normalSlots, len(candidates))
	cycleN := max(1, totalSlots/unchokeCycleFraction)
	d.unchokeCycleOffset = (d.unchokeCycleOffset + cycleN) % max(totalSlots, 1)

	var unchokeSet []*Peer

	// Fill normal slots: take top (normalSlots - cycleN) by score,
	// then cycle the remaining slots starting at cycleOffset.
	topN := max(normalSlots-cycleN, 0)

	for i := 0; i < topN && i < len(candidates); i++ {
		unchokeSet = append(unchokeSet, candidates[i].peer)
	}

	// Cycled slots: starting from offset, wrapping around.
	added := topN
	for i := 0; i < cycleN && added < normalSlots && added < len(candidates); i++ {
		idx := (d.unchokeCycleOffset + i) % len(candidates)
		// Skip if already selected.
		alreadySelected := slices.Contains(unchokeSet, candidates[idx].peer)
		if !alreadySelected {
			unchokeSet = append(unchokeSet, candidates[idx].peer)
			added++
		}
	}

	// ── Optimistic unchoke slot ─────────────────────────────────────
	// Pick a random peer NOT already in the unchoke set.
	if optimisticUnchokeN > 0 && len(candidates) > len(unchokeSet) {
		// Collect peers not in unchokeSet.
		var remaining []*Peer
		inSet := make(map[*Peer]bool, len(unchokeSet))
		for _, p := range unchokeSet {
			inSet[p] = true
		}
		for _, c := range candidates {
			if !inSet[c.peer] {
				remaining = append(remaining, c.peer)
			}
		}
		if len(remaining) > 0 {
			optIdx := d.unchokeSlotIdx % len(remaining)
			unchokeSet = append(unchokeSet, remaining[optIdx])
			d.unchokeSlotIdx++
		}
	}

	// ── Apply ───────────────────────────────────────────────────────
	for _, p := range unchokeSet {
		if p.ourChoking.CompareAndSwap(true, false) {
			p.lastUnchokeAt.Store(now)
			go p.sendEventX(Event{Event: proto.Unchoke})
		}
	}

	// Choke everyone not in the unchoke set.
	inSet := make(map[*Peer]bool, len(unchokeSet))
	for _, p := range unchokeSet {
		inSet[p] = true
	}
	for _, c := range candidates {
		if !inSet[c.peer] {
			if c.peer.ourChoking.CompareAndSwap(false, true) {
				go c.peer.sendEventX(Event{Event: proto.Choke})
			}
		}
	}

	// Trigger response scheduling for newly unchoked peers.
	select {
	case d.scheduleResponseSignal <- empty.Empty{}:
	default:
	}
}

// setInterested is called when our interest in a peer changes.
// If we become interested and the peer has free slots, we may unchoke
// immediately (libtorrent's fast-path for new peers).
func (d *Download) onPeerInterested(p *Peer) {
	if !p.peerInterested.Load() || p.snubbed.Load() || p.closed.Load() {
		return
	}

	// Count currently unchoked peers.
	unchoked := 0
	d.peers.Range(func(addr netip.AddrPort, p2 *Peer) bool {
		if !p2.closed.Load() && !p2.ourChoking.Load() {
			unchoked++
		}
		return true
	})

	if unchoked >= uploadSlots {
		return
	}

	// Fast unchoke: new peer, slots available.
	if p.ourChoking.CompareAndSwap(true, false) {
		p.lastUnchokeAt.Store(time.Now())
		go p.sendEventX(Event{Event: proto.Unchoke})

		select {
		case d.scheduleResponseSignal <- empty.Empty{}:
		default:
		}
	}
}

// ── Upload dispatch helpers ────────────────────────────────────────────

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
