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
	"time"

	"github.com/sourcegraph/conc"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

type cacheKey struct {
	hash  metainfo.Hash
	index uint32
}

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
// Based on libtorrent's choking algorithm:
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

			if !d.wait(Downloading | Seeding) {
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

// backgroundReqHandler processes upload requests from peers in FIFO order.
// Unlike the previous implementation, it:
// - Respects choking (only serves unchoked peers)
// - Processes multiple pieces per cycle
// - Uses piece caching to avoid redundant disk reads.
func (d *Download) backgroundReqHandler() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleResponseSignal:
			if !d.wait(Downloading | Seeding) {
				continue
			}

			d.processUploadRequests()
		}
	}
}

func (d *Download) processUploadRequests() {
	// collect all requests from unchoked, interested peers
	type peerRequest struct {
		peer  *Peer
		req   proto.ChunkRequest
		order int // FIFO order
	}

	var requests []peerRequest
	order := 0

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.closed.Load() {
			return true
		}
		// only serve unchoked peers
		if p.ourChoking.Load() {
			return true
		}
		p.peerRequests.Range(func(req proto.ChunkRequest, _ empty.Empty) bool {
			requests = append(requests, peerRequest{peer: p, req: req, order: order})
			order++
			return true
		})
		return true
	})

	if len(requests) == 0 {
		return
	}

	// sort by FIFO order (first requested, first served)
	slices.SortFunc(requests, func(a, b peerRequest) int {
		return a.order - b.order
	})

	// group by piece for efficient disk reads
	type pieceRequest struct {
		requests []peerRequest
		index    uint32
	}

	pieceMap := make(map[uint32]*pieceRequest)
	var pieceOrder []uint32
	for _, r := range requests {
		idx := r.req.PieceIndex
		if _, ok := pieceMap[idx]; !ok {
			pieceMap[idx] = &pieceRequest{index: idx}
			pieceOrder = append(pieceOrder, idx)
		}
		pieceMap[idx].requests = append(pieceMap[idx].requests, r)
	}

	// process pieces in FIFO order (by first request arrival)
	var cachedPieceIdx uint32
	var cachedBuf *mempool.Buffer
	cachedPieceIdx = ^uint32(0) // invalid sentinel

	defer func() {
		if cachedBuf != nil {
			mempool.Put(cachedBuf)
		}
	}()

	for _, pieceIdx := range pieceOrder {
		pr := pieceMap[pieceIdx]

		// read piece (use cache if same piece requested consecutively)
		if cachedPieceIdx != pieceIdx || cachedBuf == nil {
			if cachedBuf != nil {
				mempool.Put(cachedBuf)
			}
			cachedBuf = mempool.GetWithCap(int(d.pieceLength(pieceIdx)))
			cachedBuf.Reset()

			if err := d.readPiece(pieceIdx, cachedBuf.B); err != nil {
				d.setError(err)
				mempool.Put(cachedBuf)
				cachedBuf = nil
				continue
			}
			cachedPieceIdx = pieceIdx
		}

		// serve all requests for this piece
		var g conc.WaitGroup
		for _, r := range pr.requests {
			p := r.peer
			req := r.req

			// verify request is still valid
			if _, ok := p.peerRequests.LoadAndDelete(req); !ok {
				continue // already served or cancelled
			}

			d.ioUp.Update(int(req.Length))
			d.c.ioUp.Update(int(req.Length))
			d.uploaded.Add(int64(req.Length))

			g.Go(func() {
				p.Response(&proto.ChunkResponse{
					Data:       cachedBuf.B[req.Begin : req.Begin+req.Length],
					Begin:      req.Begin,
					PieceIndex: pieceIdx,
				})
			})
		}
		g.Wait()
	}
}

// buf must be big enough to read whole piece.
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
