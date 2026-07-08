// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"fmt"
	"slices"
	"time"

	"github.com/trim21/errgo"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
	"neptune/internal/pkg/as"
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

func (d *Download) backgroundReqScheduler() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleRequestSignal:
		}

		if !d.wait(Downloading) {
			continue
		}

		// Endgame is set inside requestABlock when remaining pieces are low
		// or a peer runs out of free blocks.
		// We do not reset it here — peer self-driven scheduling handles the
		// normal case; this goroutine is a safety net for edge cases
		// (new peers from tracker/PEX, timeout recovery).
		d.peers.Range(func(_ uint64, p Peer) bool {
			if p.Closed() {
				return true
			}
			d.requestABlock(p)
			return true
		})
	}
}

func (d *Download) have(index uint32) {
	tasks.Submit(func() {
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

func (r responseChunk) Less(o responseChunk) bool {
	return r.pi < o.pi
}

// maxMergeBlocks is the upper bound for contiguous block coalescing.
// Smaller values reduce handler latency; larger values reduce syscall count.
// Cap at 32 blocks (~512 KiB) to keep flush pauses short.
const maxMergeBlocks = 10
const maxChunkAge = 5 * time.Second

func (d *Download) backgroundResHandler() {
	d.chunk.heap = heap.Heap[responseChunk]{}
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.stateCond.C:
			// State changed (e.g. pause). If we are no longer downloading,
			// drain any remaining chunks so they are not lost.
			if d.GetState() != Downloading && d.chunk.heap.Len() > 0 {
				d.drainHeap()
			}
		case res := <-d.resChan:
			if d.GetState() != Downloading {
				d.drainHeap()
				continue
			}

			d.handleRes(res)

			// Handle the case where state changed during handleRes.
			if d.GetState() != Downloading && d.chunk.heap.Len() > 0 {
				d.drainHeap()
			}
		}
	}
}

// drainHeap flushes all remaining chunks in the heap to disk.
// Callers must ensure the handler goroutine is not concurrently
// modifying the heap (i.e. state has transitioned away from Downloading).
func (d *Download) drainHeap() {
	for d.chunk.heap.Len() > 0 {
		d.flushContiguousFromHeap()
	}
}

const defaultChunkHeapSizeLimit = 1000

var pieceChunksPool = gsync.NewPool(func() *mempool.Buffer {
	return &mempool.Buffer{
		B: make([]byte, defaultBlockSize*10),
	}
})

func (d *Download) handleRes(res *proto.ChunkResponse) {
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
		return
	}

	// Mark block as writing in the picker
	blockIndex := int(res.Begin / uint32(defaultBlockSize))
	d.picker.Load().markAsResponded(res.PieceIndex, blockIndex)

	c := responseChunk{
		res:    res,
		pi:     res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen,
		recvAt: time.Now(),
	}

	d.chunk.heap.Push(c)
	d.chunk.pending.Set(c.pi)

	// Check if this chunk completes a piece — extract and flush early.
	piecePiStart := res.PieceIndex * d.normalChunkLen
	piecePiEnd := piecePiStart + uint32(d.info.PieceBlockCount(res.PieceIndex))
	allAccounted := true
	for i := piecePiStart; i < piecePiEnd; i++ {
		d.chunk.mu.RLock()
		p := d.chunk.pending.Contains(i)
		done := d.chunk.done.Contains(i)
		d.chunk.mu.RUnlock()
		if !p && !done {
			allAccounted = false
			break
		}
	}
	if allAccounted {
		d.handlePieceFromHeap(res.PieceIndex)
		return
	}

	// Flush when heap is full or oldest chunk is older than maxChunkAge.
	oldestAge := time.Since(d.chunk.heap.Data[0].recvAt)
	if d.chunk.heap.Len() >= defaultChunkHeapSizeLimit || oldestAge > maxChunkAge {
		d.flushContiguousFromHeap()
	}
}

// flushContiguousFromHeap pops the head of the chunk heap and merges as many
// contiguous blocks as possible (up to maxMergeBlocks), then writes them to
// disk in a single call. Completed pieces are checked after the write.
func (d *Download) flushContiguousFromHeap() {
	head := d.chunk.heap.Pop()
	headPi := head.pi
	headPiece := head.res.PieceIndex

	mergedChunk := pieceChunksPool.Get()
	defer pieceChunksPool.Put(mergedChunk)
	mergedChunk.Reset()

	d.chunk.mu.Lock()
	d.chunk.done.Set(headPi)
	d.chunk.pending.Remove(headPi)
	d.chunk.mu.Unlock()

	tailPi := headPi

	mergedChunk.Write(head.res.Data)

	proto.PiecePool.Put(head.res)

	mergeLimit := d.mergeLimit()

	for d.chunk.heap.Len() != 0 {
		peak := d.chunk.heap.Peek()
		if tailPi+1 != peak.pi {
			break
		}

		if tailPi-headPi >= mergeLimit {
			break
		}

		tailPi++

		d.chunk.mu.Lock()
		d.chunk.done.Set(tailPi)
		d.chunk.pending.Remove(tailPi)
		d.chunk.mu.Unlock()

		mergedChunk.Write(peak.res.Data)

		d.chunk.heap.Pop()
		proto.PiecePool.Put(peak.res)
	}

	err := d.store.WriteChunk(headPiece, head.res.Begin, mergedChunk.B)
	if err != nil {
		return
	}

	// Mark only the blocks that were actually written as finished.
	// track which pieces were fully completed.
	var completedPieces []uint32
	for pi := headPi; pi <= tailPi; pi++ {
		pieceIdx := pi / d.normalChunkLen

		if d.checkPieceBitmapDone(pieceIdx) {
			// avoid duplicates
			already := slices.Contains(completedPieces, pieceIdx)
			if !already {
				completedPieces = append(completedPieces, pieceIdx)
			}
		}
	}

	for _, pieceIndex := range completedPieces {
		tasks.Submit(func() {
			err := d.checkPiece(pieceIndex)
			if err != nil {
				return
			}

			d.checkDone()
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
func (d *Download) handlePieceFromHeap(index uint32) {
	// Collect unique chunks for this piece from the heap.
	// In endgame mode duplicate chunks may arrive; dedup by pi.
	piecePiStart := index * d.normalChunkLen
	piecePiEnd := piecePiStart + uint32(d.info.PieceBlockCount(index))

	var pendingChunks []*proto.ChunkResponse
	seen := make([]bool, piecePiEnd-piecePiStart)

	for _, chunk := range d.chunk.heap.Data {
		if chunk.res.PieceIndex == index {
			rel := chunk.pi - piecePiStart
			if !seen[rel] {
				seen[rel] = true
				pendingChunks = append(pendingChunks, chunk.res)
			}
		}
	}

	// Count already-done chunks for this piece.
	d.chunk.mu.RLock()
	doneCount := 0
	for i := piecePiStart; i < piecePiEnd; i++ {
		if d.chunk.done.Contains(i) {
			doneCount++
		}
	}
	d.chunk.mu.RUnlock()

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
	filtered := d.chunk.heap.Data[:0]
	for _, chunk := range d.chunk.heap.Data {
		if chunk.res.PieceIndex != index {
			filtered = append(filtered, chunk)
		}
	}
	// Rebuild the heap invariant so that subsequent Pop/Peek/Push
	// calls in flushContiguousFromHeap operate on valid state.
	d.chunk.heap = *heap.FromSlice(filtered)

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
			d.chunk.mu.Lock()
			d.chunk.done.Set(pi)
			d.chunk.pending.Remove(pi)
			d.chunk.mu.Unlock()
			proto.PiecePool.Put(res)
		}

		err := d.store.WriteChunk(index, 0, buf.B)
		if err != nil {
			return
		}
	} else {
		// Mixed case: some chunks were already flushed by flushContiguousFromHeap.
		// Write each pending chunk at its correct offset.
		for _, res := range pendingChunks {
			err := d.store.WriteChunk(index, res.Begin, res.Data)
			if err != nil {
				return
			}
			pi := res.Begin/defaultBlockSize + index*d.normalChunkLen
			d.chunk.mu.Lock()
			d.chunk.done.Set(pi)
			d.chunk.pending.Remove(pi)
			d.chunk.mu.Unlock()
			proto.PiecePool.Put(res)
		}
	}

	tasks.Submit(func() {
		err := d.checkPiece(index)
		if err != nil {
			return
		}

		d.checkDone()
	})
}

func (d *Download) checkPieceBitmapDone(index uint32) bool {
	pieceCidStart := index * d.normalChunkLen
	pieceCidEnd := pieceCidStart + uint32(d.info.PieceBlockCount(index))

	d.chunk.mu.RLock()
	defer d.chunk.mu.RUnlock()

	for i := pieceCidStart; i < pieceCidEnd; i++ {
		if !d.chunk.done.Contains(i) {
			return false
		}
	}

	return true
}

func (d *Download) checkPiece(pieceIndex uint32) error {
	ok, err := d.store.VerifyPiece(pieceIndex, d.info.Pieces[pieceIndex])
	if err != nil {
		return errgo.Wrap(err, "failed to verify piece")
	}

	pieceSize := d.info.PieceLen(pieceIndex)

	if !ok {
		// Piece hash check failed — reset in picker so blocks can be re-requested
		d.picker.Load().resetPiece(pieceIndex, d.info)
		d.corruptedPiecesMu.Lock()
		d.corruptedPieces[pieceIndex]++
		d.corruptedPiecesMu.Unlock()
		d.corrupted.Add(pieceSize)
		start := pieceIndex * d.normalChunkLen
		end := start + uint32(d.info.PieceBlockCount(pieceIndex))
		d.chunk.mu.Lock()
		for i := start; i < end; i++ {
			d.chunk.done.Remove(i)
		}
		d.chunk.mu.Unlock()
		// Trigger reschedule for re-requesting this piece
		select {
		case d.scheduleRequestSignal <- empty.Empty{}:
		default:
		}
		return nil
	}

	notHave := d.completedBm.SetX(pieceIndex)

	// Mark piece as fully owned in the picker
	d.picker.Load().weHave(pieceIndex, d.info)

	if notHave {
		d.completed.Add(pieceSize)
		d.corruptedPiecesMu.Lock()
		delete(d.corruptedPieces, pieceIndex)
		d.corruptedPiecesMu.Unlock()
		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)

		// Notify scheduler that piece completed so peers can request new blocks.
		select {
		case d.scheduleRequestSignal <- empty.Empty{}:
		default:
		}
	}

	return nil
}

func (d *Download) checkDone() {
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
	d.CompletedAt.Store(time.Now().Unix())
	d.pieceDownloadRate.Reset()

	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.PeerBitmap().Count() == d.info.NumPieces {
			p.Close()
		}

		return true
	})

	d.Trk.Announce(tracker.EventCompleted)

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
	d.picker.Load().resetAll()
	d.completed.Store(0)
	d.stateCond.Broadcast()

	d.runHashCheck(func() {
		d.CompletedAt.Store(time.Now().Unix())

		d.peers.Range(func(_ uint64, p Peer) bool {
			if p.PeerBitmap().Count() == d.info.NumPieces {
				p.Close()
			}
			return true
		})

		d.Trk.Announce(tracker.EventCompleted)
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

// requestABlock picks blocks for a single peer using the global piece picker.
// Mirrors libtorrent's request_a_block().
//
// It determines the desired queue size dynamically (rate-based or snubbed=1),
// calls the piece picker to find blocks, filters out already-queued blocks,
// and pushes free blocks into the peer's request channel. In endgame mode,
// it may also pick busy blocks (already requested by other peers).
func (d *Download) requestABlock(p Peer) {
	if d.completedBm.Count() == d.info.NumPieces {
		return
	}
	if p.Closed() {
		return
	}
	if !d.HasState(Downloading) {
		return
	}

	desiredQueueSize := p.DesiredQueueSize()
	myReq := p.OutstandingRequests()
	reqQ := p.QueueLen()

	numRequests := desiredQueueSize - myReq - reqQ
	if numRequests <= 0 {
		if d.session.Debug {
			s := fmt.Sprintf("skip: numReq=0 (desired=%d, myReq=%d, reqQ=%d)", desiredQueueSize, myReq, reqQ)
			p.SetLastPickDebug(s)
		}
		return
	}

	choked := p.IsChoking()
	peerBitmap := p.PeerBitmap()
	allowFastBm := p.FastBitmap()
	lastPick := p.LastPickResult()

	pickResult := d.picker.Load().pickPieces(
		peerBitmap,
		choked,
		allowFastBm,
		numRequests,
		0,
		nil,
		d.info,
		lastPick,
	)

	p.SetLastPickResult(pickResult)

	// If the peer is choked and no fast pieces are allowed, the picker returns
	// zero blocks — don't mistake this for "no blocks left" and trigger endgame.
	if len(pickResult.freeBlocks) == 0 && choked && allowFastBm.Count() == 0 {
		if d.session.Debug {
			s := fmt.Sprintf("choked no fast: numReq=%d, desired=%d", numRequests, desiredQueueSize)
			p.SetLastPickDebug(s)
		}
		return
	}

	// add_request: push picked blocks directly to requestQueue.
	freeBlocksPicked := 0
	skippedInQueue := 0
	skippedFinished := 0
	skippedCompleted := 0
	skippedDone := 0
	for _, fb := range pickResult.freeBlocks {
		if numRequests <= 0 {
			break
		}

		chunk := pieceChunk(d.info, fb.pieceIndex, fb.blockIndex)
		if p.IsInQueue(chunk) {
			skippedInQueue++
			continue
		}

		if d.picker.Load().isFinished(fb.pieceIndex, fb.blockIndex) {
			skippedFinished++
			continue
		}

		if d.completedBm.Contains(fb.pieceIndex) {
			skippedCompleted++
			continue
		}

		chunkPi := fb.pieceIndex*d.normalChunkLen + uint32(fb.blockIndex)
		d.chunk.mu.RLock()
		done := d.chunk.done.Contains(chunkPi)
		d.chunk.mu.RUnlock()
		if done {
			skippedDone++
			continue
		}

		p.EnqueueBlock(fb.pieceIndex, fb.blockIndex)
		enqueueBlockDelay()
		// Re-check: Close() may have called abortDownload for this block
		// between EnqueueBlock and now. Don't re-mark as requesting.
		if p.Closed() {
			numRequests--
			continue
		}
		d.picker.Load().markAsRequesting(fb.pieceIndex, fb.blockIndex)
		d.picker.Load().addDownloadingPiece(fb.pieceIndex, d.info)

		numRequests--
		freeBlocksPicked++
	}

	if d.session.Debug {
		skipTotal := skippedInQueue + skippedFinished + skippedCompleted + skippedDone
		if skipTotal > 0 {
			s := fmt.Sprintf("picked=%d/%d free, skip: inQ=%d fin=%d done=%d compl=%d",
				freeBlocksPicked, len(pickResult.freeBlocks),
				skippedInQueue, skippedFinished, skippedDone, skippedCompleted)
			p.SetLastPickDebug(s)
		} else {
			s := fmt.Sprintf("picked=%d/%d free, %d busy", freeBlocksPicked, len(pickResult.freeBlocks), len(pickResult.busyBlocks))
			p.SetLastPickDebug(s)
		}
	}

	// send_block_requests: drain requestQueue immediately.
	p.SendBlockRequests()

	if numRequests <= 0 {
		return
	}

	if len(pickResult.busyBlocks) == 0 {
		return
	}

	for _, bb := range pickResult.busyBlocks {
		chunk := pieceChunk(d.info, bb.pieceIndex, bb.blockIndex)
		if p.IsInQueue(chunk) {
			continue
		}

		if d.picker.Load().isFinished(bb.pieceIndex, bb.blockIndex) {
			continue
		}

		if d.completedBm.Contains(bb.pieceIndex) {
			continue
		}

		chunkPi := bb.pieceIndex*d.normalChunkLen + uint32(bb.blockIndex)
		d.chunk.mu.RLock()
		done := d.chunk.done.Contains(chunkPi)
		d.chunk.mu.RUnlock()
		if done {
			continue
		}

		p.EnqueueBlock(bb.pieceIndex, bb.blockIndex)
		enqueueBlockDelay()
		if p.Closed() {
			continue
		}
		d.picker.Load().markAsRequesting(bb.pieceIndex, bb.blockIndex)
		d.picker.Load().addDownloadingPiece(bb.pieceIndex, d.info)

		// Drain after adding a busy block too.
		p.SendBlockRequests()
		return
	}
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
