// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"slices"
	"sync"
	"time"

	"github.com/trim21/errgo"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

// minRequestQueue is the minimum number of outstanding requests per peer.
const minRequestQueue = 2

// maxRequestQueue is the maximum number of outstanding requests per peer.
const maxRequestQueue = 2000

// scheduler is a private interface satisfied by *peerImpl.
// It allows notifyPeersToRequest to trigger re-request without
// exposing scheduling methods on PeerInterface.
type scheduler interface {
	requestABlock()
}

func (d *Download) notifyPeersToRequest() {
	if !d.IsActiveDownloading() {
		return
	}
	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.Closed() {
			return true
		}
		if s, ok := any(p).(scheduler); ok {
			s.requestABlock()
		}
		return true
	})
}

func (d *Download) have(index uint32) {
	tasks.SubmitNet(func() {
		d.peers.Range(func(_ uint64, p Peer) bool {
			p.Have(index)
			return true
		})
	})
}

type responseChunk struct {
	recvAt time.Time
	res    *proto.ChunkResponse
	pi     uint32
}

type peerContributors struct {
	m  map[uint32]map[uint64]empty.Empty
	mu sync.Mutex
}

func (r responseChunk) Less(o responseChunk) bool {
	return r.pi < o.pi
}

// maxMergeBlocks is the upper bound for contiguous block coalescing.
// Smaller values reduce handler latency; larger values reduce syscall count.
// Cap at 32 blocks (~512 KiB) to keep flush pauses short.
const maxMergeBlocks = 10
const maxChunkAge = 5 * time.Second

func (d *Download) backgroundResHandler() {
	defer d.log.Info().Msg("backgroundResHandler: exiting")
	var h heap.Heap[responseChunk]
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}
	done := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pending := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	for {
		select {
		case <-d.ctx.Done():
			d.log.Info().Msg("backgroundResHandler: exiting (ctx canceled)")
			return
		case <-d.stateCond.C:
			// State changed (e.g. pause). If we are no longer downloading,
			// drain any remaining chunks so they are not lost.
			if d.GetState() != Downloading && h.Len() > 0 {
				drainHeap(d, &h, pc, done, pending)
			}
			// Re-init bitmaps if re-entering Downloading (e.g. after
			// recheck found corruption in a previously seeding torrent).
			if d.IsDownloading() {
				totalBlocks := d.info.TotalBlockCount()
				done.Init(totalBlocks)
				pending.Init(totalBlocks)
			}
		case res := <-d.resChan:
			if d.GetState() != Downloading {
				drainHeap(d, &h, pc, done, pending)
				continue
			}

			handleRes(d, &h, pc, done, pending, res)

			// Handle the case where state changed during handleRes.
			if d.GetState() != Downloading && h.Len() > 0 {
				drainHeap(d, &h, pc, done, pending)
			}
		}
	}
}

// drainHeap flushes all remaining chunks in the heap to disk.
// All callers are on the backgroundResHandler goroutine.
func drainHeap(d *Download, h *heap.Heap[responseChunk], pc *peerContributors, done, pending *bm.NilSafeLockFreeBitmap) {
	heapLen := h.Len()
	d.log.Info().Int("chunks", heapLen).Msg("drainHeap: start flushing remaining chunks")
	for h.Len() > 0 {
		flushContiguousFromHeap(d, h, pc, done, pending)
	}
	d.log.Info().Int("chunks", heapLen).Msg("drainHeap: done")
	// Reset heap to release backing array. download completed → seeding,
	// this goroutine keeps running so the local var won't be GC'd.
	*h = heap.Heap[responseChunk]{}
}

const defaultChunkHeapSizeLimit = 5000

var pieceChunksPool = gsync.NewPool(func() *mempool.Buffer {
	return &mempool.Buffer{
		B: make([]byte, defaultBlockSize*10),
	}
})

func handleRes(d *Download, h *heap.Heap[responseChunk], pc *peerContributors, done, pending *bm.NilSafeLockFreeBitmap, submit chunkSubmit) {
	res := submit.res
	d.log.Trace().
		Int("length", len(res.Data)).
		Uint32("offset", res.Begin).
		Uint32("piece", res.PieceIndex).
		Msg("res received")

	// Rate limit: global first, then per-torrent.
	// Global acts as the primary clock; per-torrent is a secondary constraint.
	// This order prevents the global limiter from accumulating burst while the
	// per-torrent limiter blocks, avoiding boom-bust oscillation.
	if err := d.session.DownloadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		return
	}
	if err := d.downloadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		return
	}

	d.pieceDownloadRate.Update(len(res.Data))
	d.session.PieceDownloadRate.Update(len(res.Data))
	d.downloaded.Add(int64(len(res.Data)))

	// in endgame mode we may receive duplicated response, just ignore them
	if d.completedBm.Contains(res.PieceIndex) {
		d.wastedStale.Add(int64(len(res.Data)))
		return
	}

	// Discard duplicate blocks that have already been flushed to disk
	// (endgame race). Overwriting would replace the first peer's data.
	cPi := res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen
	if done.Contains(cPi) {
		d.wastedDupe.Add(int64(len(res.Data)))
		proto.PiecePool.Put(res)
		return
	}

	c := responseChunk{
		res:    res,
		pi:     cPi,
		recvAt: time.Now(),
	}

	// Record which peer contributed to this piece — after all early returns
	// so dropped chunks don't leave stale entries in pieceContributors.
	pc.mu.Lock()
	peers, ok := pc.m[res.PieceIndex]
	if !ok {
		peers = make(map[uint64]empty.Empty)
		pc.m[res.PieceIndex] = peers
	}
	peers[submit.peerID] = empty.Empty{}
	pc.mu.Unlock()

	pending.Set(c.pi)
	d.pendingBytes.Add(int64(len(res.Data)))

	// Mark block as writing in the picker
	blockIndex := int(res.Begin / uint32(defaultBlockSize))
	d.picker.Load().MarkAsResponded(res.PieceIndex, blockIndex)

	h.Push(c)

	// Check if this chunk completes a piece — extract and flush early.
	piecePiStart := res.PieceIndex * d.normalChunkLen
	piecePiEnd := piecePiStart + uint32(d.info.PieceBlockCount(res.PieceIndex))
	allAccounted := true
	for i := piecePiStart; i < piecePiEnd; i++ {
		p := pending.Contains(i)
		done := done.Contains(i)
		if !p && !done {
			allAccounted = false
			break
		}
	}
	if allAccounted {
		handlePieceFromHeap(d, h, pc, done, pending, res.PieceIndex)
		return
	}

	// Refill request queues only after the picker has observed this response.
	// Scheduling from the peer read loop races ahead of MarkAsResponded and can
	// see a full queue, leaving no later event to request the next block.
	d.notifyPeersToRequest()

	// Flush when heap is full or oldest chunk is older than maxChunkAge.
	oldestAge := time.Since(h.Data[0].recvAt)
	if h.Len() >= defaultChunkHeapSizeLimit || oldestAge > maxChunkAge {
		flushContiguousFromHeap(d, h, pc, done, pending)
	}
}

// flushContiguousFromHeap pops the head of the chunk heap and merges as many
// contiguous blocks as possible (up to maxMergeBlocks), then writes them to
// disk in a single call. Completed pieces are checked after the write.
func flushContiguousFromHeap(d *Download, h *heap.Heap[responseChunk], pc *peerContributors, done, pending *bm.NilSafeLockFreeBitmap) {
	head := h.Pop()
	headPi := head.pi
	headPiece := head.res.PieceIndex

	mergedChunk := pieceChunksPool.Get()
	defer pieceChunksPool.Put(mergedChunk)
	mergedChunk.Reset()

	done.Set(headPi)
	pending.Unset(headPi)
	d.pendingBytes.Add(-defaultBlockSize)

	tailPi := headPi

	mergedChunk.Write(head.res.Data)

	proto.PiecePool.Put(head.res)

	mergeLimit := d.mergeLimit()

	for h.Len() != 0 {
		peak := h.Peek()
		if tailPi+1 != peak.pi {
			break
		}

		if tailPi-headPi >= mergeLimit {
			break
		}

		tailPi++

		done.Set(tailPi)
		pending.Unset(tailPi)
		d.pendingBytes.Add(-defaultBlockSize)

		mergedChunk.Write(peak.res.Data)

		h.Pop()
		proto.PiecePool.Put(peak.res)
	}

	err := d.store.WriteChunk(d.ctx, headPiece, head.res.Begin, mergedChunk.B)
	if err != nil {
		d.setError(err)
		return
	}

	// Mark only the blocks that were actually written as finished.
	// track which pieces were fully completed.
	var completedPieces []uint32
	for pi := headPi; pi <= tailPi; pi++ {
		pieceIdx := pi / d.normalChunkLen

		if d.checkPieceBitmapDone(pieceIdx, done) {
			// avoid duplicates
			already := slices.Contains(completedPieces, pieceIdx)
			if !already {
				completedPieces = append(completedPieces, pieceIdx)
			}
		}
	}

	for _, pieceIndex := range completedPieces {
		tasks.SubmitIO(func() {
			err := d.checkPiece(pieceIndex, pc, done)
			if err != nil {
				d.setError(err)
				return
			}

			d.checkDone(done, pending)
		})
	}
}

// mergeLimit returns the maximum number of contiguous blocks to merge in one
// write. On fast storage more blocks can be merged to reduce syscall count;
// on slow storage a smaller limit avoids blocking the handler too long.
func (d *Download) mergeLimit() uint32 {
	rate := d.ioDownloadRate.Status().CurRate
	if rate <= 0 {
		return maxMergeBlocks
	}
	// Estimate how many blocks can be written in ~1ms.
	msBlocks := uint32(rate / defaultBlockSize / 1000)
	if msBlocks < 4 {
		return 4
	}
	if msBlocks > 32 {
		return 32
	}
	return msBlocks
}

// find all chunks from chunkHeap and write them to disk.
// Handles two cases:
//   - all chunks are pending (in heap): writes the full piece in one call.
//   - some chunks were already flushed by flushContiguousFromHeap: writes only
//     the pending chunks at their respective offsets, then verifies the piece.
func handlePieceFromHeap(d *Download, h *heap.Heap[responseChunk], pc *peerContributors, done, pending *bm.NilSafeLockFreeBitmap, index uint32) {
	// Collect unique chunks for this piece from the heap.
	// In endgame mode duplicate chunks may arrive; dedup by pi.
	piecePiStart := index * d.normalChunkLen
	piecePiEnd := piecePiStart + uint32(d.info.PieceBlockCount(index))

	var pendingChunks []*proto.ChunkResponse
	// seen tracks which block pis we already have (from heap OR done bitmap).
	// Pre-populate from done bitmap so endgame duplicates don't cause
	// double-counting when a block is both done and pending.
	seen := make([]bool, piecePiEnd-piecePiStart)
	for i := piecePiStart; i < piecePiEnd; i++ {
		seen[i-piecePiStart] = done.Contains(i)
	}
	doneCount := 0
	for _, s := range seen {
		if s {
			doneCount++
		}
	}

	for _, chunk := range h.Data {
		if chunk.res.PieceIndex == index {
			rel := chunk.pi - piecePiStart
			if !seen[rel] {
				seen[rel] = true
				pendingChunks = append(pendingChunks, chunk.res)
			} else {
				d.wastedDupe.Add(int64(len(chunk.res.Data)))
			}
		}
	}

	totalNeeded := d.info.PieceBlockCount(index)
	if len(pendingChunks)+doneCount != totalNeeded {
		return
	}

	// No pending chunks — everything already flushed.
	if len(pendingChunks) == 0 {
		return
	}

	// Remove all chunks belonging to the completed piece from the heap.
	// In-place filter: keep only chunks with a different piece index.
	filtered := h.Data[:0]
	for _, chunk := range h.Data {
		if chunk.res.PieceIndex != index {
			filtered = append(filtered, chunk)
		}
	}
	// Rebuild the heap invariant so that subsequent Pop/Peek/Push
	// calls in flushContiguousFromHeap operate on valid state.
	*h = *heap.FromSlice(filtered)

	if doneCount == 0 {
		// Sort pending chunks by Begin offset — they were collected from the
		// heap backing array which is not fully sorted (only the root is guaranteed
		// to be the minimum). Writing them out of order produces a corrupted piece.
		slices.SortFunc(pendingChunks, func(a, b *proto.ChunkResponse) int {
			return int(a.Begin) - int(b.Begin)
		})

		// Fast path: all chunks are pending, write the full piece at once.
		buf := mempool.GetWithCap(int(d.info.PieceLen(index)))
		defer mempool.Put(buf)
		buf.Reset()

		for _, res := range pendingChunks {
			buf.Write(res.Data)
			pi := res.Begin/defaultBlockSize + index*d.normalChunkLen
			done.Set(pi)
			pending.Unset(pi)
			d.pendingBytes.Add(-defaultBlockSize)
			proto.PiecePool.Put(res)
		}

		err := d.store.WriteChunk(d.ctx, index, 0, buf.B)
		if err != nil {
			d.setError(err)
			return
		}
	} else {
		// Mixed case: some chunks were already flushed by flushContiguousFromHeap.
		// Write each pending chunk at its correct offset.
		for _, res := range pendingChunks {
			err := d.store.WriteChunk(d.ctx, index, res.Begin, res.Data)
			if err != nil {
				d.setError(err)
				return
			}
			pi := res.Begin/defaultBlockSize + index*d.normalChunkLen
			done.Set(pi)
			pending.Unset(pi)
			d.pendingBytes.Add(-defaultBlockSize)
			proto.PiecePool.Put(res)
		}
	}

	tasks.SubmitIO(func() {
		err := d.checkPiece(index, pc, done)
		if err != nil {
			d.setError(err)
			return
		}

		d.checkDone(done, pending)
	})
}

func (d *Download) checkPieceBitmapDone(index uint32, done *bm.NilSafeLockFreeBitmap) bool {
	pieceCidStart := index * d.normalChunkLen
	pieceCidEnd := pieceCidStart + uint32(d.info.PieceBlockCount(index))

	for i := pieceCidStart; i < pieceCidEnd; i++ {
		if !done.Contains(i) {
			return false
		}
	}

	return true
}

func (d *Download) checkPiece(pieceIndex uint32, pc *peerContributors, done *bm.NilSafeLockFreeBitmap) error {
	ok, err := d.store.VerifyPiece(d.ctx, pieceIndex, d.info.Pieces[pieceIndex])
	if err != nil {
		return errgo.Wrap(err, "failed to verify piece")
	}

	pieceSize := d.info.PieceLen(pieceIndex)

	if !ok {
		// Piece hash check failed — reset in picker so blocks can be re-requested
		d.picker.Load().ResetPiece(pieceIndex)
		d.corruptedPiecesMu.Lock()
		d.corruptedPieces[pieceIndex]++
		d.corruptedPiecesMu.Unlock()
		d.corrupted.Add(pieceSize)
		start := pieceIndex * d.normalChunkLen
		end := start + uint32(d.info.PieceBlockCount(pieceIndex))
		for i := start; i < end; i++ {
			done.Unset(i)
		}

		// Penalize peers that contributed blocks to this corrupt piece.
		d.penalizePiecePeers(pieceIndex, false, pc)

		// Trigger reschedule for re-requesting this piece
		d.notifyPeersToRequest()
		return nil
	}

	d.missingBm.Unset(pieceIndex)
	notHave := d.completedBm.SetX(pieceIndex)

	// Mark piece as fully owned in the picker
	d.picker.Load().WeHave(pieceIndex)

	// Reward peers that contributed to this successful piece.
	d.penalizePiecePeers(pieceIndex, true, pc)

	if notHave {
		d.completed.Add(pieceSize)
		d.corruptedPiecesMu.Lock()
		delete(d.corruptedPieces, pieceIndex)
		d.corruptedPiecesMu.Unlock()
		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)
	}

	// Hash verification removes the piece from downloadingPieces. Wake peers
	// after that state change so they can claim another piece immediately.
	d.notifyPeersToRequest()
	return nil
}

// penalizePiecePeers reads the contributors recorded in chunkState for this
// piece, calls OnHashFailed or OnHashPassed on each, then clears the record.
// Peers that did not contribute are unaffected.
//
// On hash failure: if a peer was the sole contributor, it is marked as
// bad for this piece. If all connected peers end up blocked from
// the piece, the one blocked longest ago (and not marked bad) is
// unblocked to cycle through peers until a correct one is found.
func (d *Download) penalizePiecePeers(pieceIndex uint32, passed bool, pc *peerContributors) {
	pc.mu.Lock()
	contributors := pc.m[pieceIndex]
	delete(pc.m, pieceIndex)
	pc.mu.Unlock()

	if len(contributors) == 0 {
		return
	}

	for peerID := range contributors {
		p, ok := d.peers.Load(peerID)
		if !ok {
			continue
		}
		if passed {
			p.OnHashPassed(pieceIndex)
			// Any successful piece proves the peer can deliver valid data — exit parole.
			p.SetOnParole(false)
			p.AddTrustPoints(1)
		} else {
			p.OnHashFailed(pieceIndex)

			// If this peer was the ONLY contributor, they are definitively
			// the source of corrupt data for this piece.
			if len(contributors) == 1 {
				p.SetBadPiece(pieceIndex)
			}

			// Parole mode: restrict to exclusive pieces.
			p.SetOnParole(true)
			tp := p.AddTrustPoints(-2)

			if tp <= -7 { // 4 consecutive hash-failed contributions
				d.log.Info().
					Uint64("peer_id", peerID).
					Str("addr", p.Addr().String()).
					Int32("trust_points", tp).
					Msg("banning peer: too many corrupt pieces")
				d.banAddr(p.Addr().Addr())
				p.Close()
			}
		}
	}

	// On hash fail: if all connected peers are now blocked, gradually
	// unblock the one blocked longest ago (excluding bad peers).
	if !passed {
		d.gradualUnblockPiece(pieceIndex)
	}
}

// gradualUnblockPiece checks whether every connected peer is blocked from
// pieceIndex. If so, it unblocks the peer blocked longest ago (skipping
// peers marked bad for this piece), so they can retry the piece.
func (d *Download) gradualUnblockPiece(pieceIndex uint32) {
	var (
		oldestPeer Peer
		oldestTime time.Time
	)

	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.Closed() {
			return true
		}
		if !p.IsBlocked(pieceIndex) {
			// At least one peer is not blocked — no need to unblock.
			oldestPeer = nil
			return false
		}
		if p.IsBadPiece(pieceIndex) {
			return true
		}
		t, ok := p.BlockedPieceTime(pieceIndex)
		if !ok {
			return true
		}
		if oldestPeer == nil || t.Before(oldestTime) {
			oldestPeer = p
			oldestTime = t
		}
		return true
	})

	if oldestPeer != nil {
		oldestPeer.OnHashPassed(pieceIndex)
	}
}

func (d *Download) checkDone(done, pending *bm.NilSafeLockFreeBitmap) {
	if d.completedBm.Count() != d.info.NumPieces {
		return
	}

	if d.session.RecheckOnComplete.Load() {
		d.recheckAfterComplete()
		return
	}

	if err := d.transition(Seeding); err != nil {
		d.log.Error().Err(err).Msg("failed to transition state in checkDone")
		return
	}
	// Release per-block bitmaps — no longer needed once seeding.
	done.Release()
	pending.Release()
	d.CompletedAt.Store(time.Now().UnixNano())
	d.pieceDownloadRate.Reset()

	d.fireCompletedHook()

	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.PeerBitmap().Count() == d.info.NumPieces {
			p.Close()
		}

		return true
	})

	d.tracker.Announce(tracker.EventCompleted)

	// Release picker memory — no longer needed when seeding.
	d.picker.Store(nil)
}

// recheckAfterComplete transitions to Checking, re-hashes all pieces, and
// completes the download (Seeding + announce) when all pass, or goes back to
// Downloading so corrupt pieces are re-fetched.
func (d *Download) recheckAfterComplete() {
	if err := d.transition(Checking); err != nil {
		d.log.Error().Err(err).Msg("failed to start completion recheck")
		return
	}
	d.completedBm.Clear()
	d.setMissingFromWantedSync()
	d.picker.Load().ResetAll()
	d.completed.Store(0)
	d.stateCond.Broadcast()

	d.runHashCheck(func() {
		d.CompletedAt.Store(time.Now().UnixNano())

		d.fireCompletedHook()

		d.peers.Range(func(_ uint64, p Peer) bool {
			if p.PeerBitmap().Count() == d.info.NumPieces {
				p.Close()
			}
			return true
		})

		d.tracker.Announce(tracker.EventCompleted)
		d.picker.Store(nil)
	})
}

// runHashCheck spawns a goroutine that re-hashes all pieces via initCheck.
// When all pieces pass, onSeeding is called right before transition(Seeding).
func (d *Download) runHashCheck(onSeeding func()) {
	go func() {
		if err := d.initCheck(); err != nil {
			if d.ctx.Err() != nil {
				return
			}
			d.setError(err)
			d.log.Err(err).Msg("hash check failed")
			return
		}

		d.s.mu.RLock()
		d.markUnselectedPiecesDoneUnsafe()
		d.completed.Store(d.computeCompletedUnsafe())
		d.s.mu.RUnlock()
		d.pieceDownloadRate.Reset()

		if d.completedBm.Count() == d.info.NumPieces {
			if onSeeding != nil {
				onSeeding()
			}
			if err := d.transition(Seeding); err != nil {
				d.log.Error().Err(err).Msg("failed to transition after hash check")
				return
			}
		} else {
			if err := d.transition(Downloading); err != nil {
				d.log.Error().Err(err).Msg("failed to transition after hash check")
				return
			}
		}
		d.stateCond.Broadcast()
	}()
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
