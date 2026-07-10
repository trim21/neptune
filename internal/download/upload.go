// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

type uploadReq struct {
	peer Peer
	req  proto.ChunkRequest
}

var errUploadPaused = errors.New("upload paused")

const (
	uploadSlots          = 4 // default number of upload slots
	optimisticUnchokeN   = 1 // reserved for optimistic unchoke
	UnchokeInterval      = 10 * time.Second
	unchokeGracePeriod   = 50 * time.Second // don't choke peers recently unchoked
	unchokeCycleFraction = 8                // cycle ~1/8 = ~12.5% of slots per round
)

// peerUploadScore computes a weighted score for unchoke priority.
// Aligns with libtorrent's calculate_unchoke_upload_leech_experimental:
//   - Peers that have unchoked us (reciprocating) get order_base + rate bonus
//   - Peers that haven't unchoked us get random weight (optimistic queue)
//   - Preferred peers get a multiplier
func peerUploadScore(p Peer) (score uint32, isReciprocal bool) {
	const orderBase uint32 = 1 << 30

	multiplier := uint32(1)
	if p.IsPreferred() {
		multiplier = 4
	}

	// Peer has unchoked us — they're reciprocating.
	if !p.IsChoking() {
		downRate := uint32(p.DownloadRate()) / 64
		score = orderBase + downRate*multiplier
		return score, true
	}

	// Peer hasn't unchoked us — optimistic, random weight.
	// Lower weight for stingy peers (those we upload to at < 1KB/s).
	upRate := uint32(p.UploadRate()) / 64
	if upRate < 2048/64 {
		score = upRate * multiplier
	} else {
		base := uint32(1 << 10)
		if p.IsPreferred() {
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
		peer        Peer
		score       uint32
		reciprocal  bool // peer has unchoked us
		recentlyUnc bool // unchoked within grace period
	}

	var candidates []candidate

	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.Closed() {
			return true
		}

		// Always choke snubbed peers.
		if p.IsSnubbed() {
			if p.SwapOurChoking(false, true) {
				p.SendChoke()
			}
			return true
		}

		// Always choke uninterested peers — libtorrent only queues peers
		// that explicitly signaled Interested.
		if !p.IsPeerInterested() {
			if p.SwapOurChoking(false, true) {
				p.SendChoke()
			}
			return true
		}

		score, reciprocal := peerUploadScore(p)
		recently := now.Sub(p.LastUnchokeAt()) < unchokeGracePeriod

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

	var unchokeSet []Peer

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
		var remaining []Peer
		inSet := make(map[Peer]bool, len(unchokeSet))
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
		if p.SwapOurChoking(true, false) {
			p.SetLastUnchokeAt(now)
			go p.SendUnchoke()
		}
	}

	// Choke everyone not in the unchoke set.
	inSet := make(map[Peer]bool, len(unchokeSet))
	for _, p := range unchokeSet {
		inSet[p] = true
	}
	for _, c := range candidates {
		if !inSet[c.peer] {
			if c.peer.SwapOurChoking(false, true) {
				c.peer.SendChoke()
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
func (d *Download) onPeerInterested(p Peer) {
	if !p.IsPeerInterested() || p.IsSnubbed() || p.Closed() {
		return
	}

	// Count currently unchoked peers.
	unchoked := 0
	d.peers.Range(func(_ uint64, p2 Peer) bool {
		if !p2.Closed() && !p2.IsOurChoking() {
			unchoked++
		}
		return true
	})

	if unchoked >= uploadSlots {
		return
	}

	// Fast unchoke: new peer, slots available.
	if p.SwapOurChoking(true, false) {
		p.SetLastUnchokeAt(time.Now())
		p.SendUnchoke()

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
	defer d.log.Info().Msg("backgroundReqHandler: exiting")
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
			d.log.Info().Msg("backgroundReqHandler: exiting (ctx canceled)")
			return
		case <-d.scheduleResponseSignal:
			if !d.IsActive() {
				continue
			}

			pieceOrder = pieceOrder[:0]
			clear(requestsByPiece)

			considered := 0
			d.peers.Range(func(_ uint64, p Peer) bool {
				if considered >= maxRequestsToConsiderPerWake {
					return false
				}

				if p.Closed() || p.IsOurChoking() {
					return true
				}

				if p.PeerRequestCount() == 0 {
					return true
				}

				p.ForEachPeerRequest(func(key proto.ChunkRequest, _ empty.Empty) bool {
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

func (d *Download) processUpload(peer Peer, req proto.ChunkRequest, data []byte) {
	if d.ctx.Err() != nil || peer.Closed() {
		return
	}
	if !d.IsActive() {
		return
	}

	res := proto.PiecePool.Get()
	res.PieceIndex = req.PieceIndex
	res.Begin = req.Begin

	if data != nil {
		res.Data = append(res.Data[:0], data...)
	} else {
		res.Data = slices.Grow(res.Data[:0], int(req.Length))[:req.Length]
		if err := d.readPieceRangeCtx(d.ctx, req, res.Data); err != nil {
			proto.PiecePool.Put(res)
			if err == errUploadPaused || err == context.Canceled {
				return
			}
			d.setError(err)
			peer.Close()
			return
		}
	}

	if err := d.session.UploadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		proto.PiecePool.Put(res)
		return
	}
	if err := d.uploadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		proto.PiecePool.Put(res)
		return
	}

	if peer.Response(res) {
		d.session.PieceUploadRate.Update(len(res.Data))
		d.pieceUploadRate.Update(len(res.Data))
		d.uploaded.Add(int64(len(res.Data)))
	} else {
		proto.PiecePool.Put(res)
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

	if !d.session.Enqueue(func() {
		d.processUpload(item.peer, item.req, nil)
	}) {
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
	buf := mempool.GetWithCap(int(d.info.PieceLen(index)))
	defer mempool.Put(buf)

	if _, err := d.store.ReadChunk(d.ctx, index, 0, buf.B); err != nil {
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

	if !d.session.Enqueue(func() {
		d.processUpload(item.peer, item.req, data)
	}) {
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

// readPieceRangeCtx reads a range of bytes from a piece into dst.
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

	_, err := d.store.ReadChunk(ctx, req.PieceIndex, req.Begin, dst)
	return err
}
