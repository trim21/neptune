// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"crypto/sha1"
	"io"
	"net/netip"
	"time"

	"github.com/docker/go-units"
	"github.com/trim21/errgo"

	"neptune/internal/core/tracker"
	"neptune/internal/meta"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/pkg/gslice"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

// requestQueueTime is the target number of seconds of pipeline to maintain
// (mirrors libtorrent's request_queue_time setting).
const requestQueueTime = 3

// minRequestQueue is the minimum number of outstanding requests per peer.
const minRequestQueue = 2

// maxRequestQueue is the maximum number of outstanding requests per peer.
const maxRequestQueue = 250

func (d *Download) backgroundReqScheduler() {
	timer := time.NewTimer(time.Second * 5)
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleRequestSignal:
		case <-timer.C:
		}

		if !d.wait(Downloading) {
			continue
		}

		// Iterate peers and request blocks for each
		d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
			if p.closed.Load() {
				return true
			}
			d.requestABlock(p)
			return true
		})
	}
}

func (d *Download) have(index uint32) {
	tasks.Submit(func() {
		d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
			p.Have(index)
			return true
		})
	})
}

type responseChunk struct {
	res *proto.ChunkResponse
	pi  uint32
}

func (r responseChunk) Less(o responseChunk) bool {
	return r.pi < o.pi
}

func (d *Download) backgroundResHandler() {
	d.chunk.heap = heap.Heap[responseChunk]{}
	for {
		select {
		case <-d.ctx.Done():
			return
		case res := <-d.ResChan:
			if d.GetState() != Downloading {
				continue
			}

			d.handleRes(res)
		}
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
	if err := d.c.downloadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		return
	}
	if err := d.downloadLimiter.Wait(d.ctx, len(res.Data)); err != nil {
		return
	}

	d.pieceDownloadRate.Update(len(res.Data))
	d.c.pieceDownloadRate.Update(len(res.Data))
	d.downloaded.Add(int64(len(res.Data)))


	// in endgame mode we may receive duplicated response, just ignore them
	if d.bm.Contains(res.PieceIndex) {
		return
	}

	// Mark block as writing in the picker
	blockIndex := int(res.Begin / uint32(defaultBlockSize))
	d.picker.markAsWriting(res.PieceIndex, blockIndex)

	if d.endGameMode.Load() {
		d.handleResEndgame(res)
		return
	}

	c := responseChunk{
		res: res,
		pi:  res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen,
	}

	d.chunk.heap.Push(c)
	d.chunk.pending.Set(c.pi)

	if d.chunk.heap.Len() < defaultChunkHeapSizeLimit {
		piecePiStart := res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen
		piecePiEnd := piecePiStart + uint32(pieceChunksCount(d.info, res.PieceIndex))
		for i := piecePiStart; i < piecePiEnd; i++ {
			if !d.chunk.pending.Contains(i) {
				return
			}
		}
		d.handlePieceFromHeap(res.PieceIndex)
		return
	}

	head := d.chunk.heap.Pop()
	headPi := head.pi
	headPiece := head.res.PieceIndex

	mergedChunk := pieceChunksPool.Get()
	defer pieceChunksPool.Put(mergedChunk)
	mergedChunk.Reset()

	d.chunk.mu.Lock()
	d.chunk.done.Set(headPi)
	d.chunk.mu.Unlock()

	tailPi := headPi

	start := int64(headPiece)*d.info.PieceLength + int64(head.res.Begin)

	mergedChunk.Write(head.res.Data)

	proto.PiecePool.Put(head.res)

	for d.chunk.heap.Len() != 0 {
		peak := d.chunk.heap.Peek()
		if tailPi+1 != peak.pi {
			break
		}

		if tailPi-headPi >= 10 {
			break
		}

		tailPi++

		d.chunk.mu.Lock()
		d.chunk.done.Set(tailPi)
		d.chunk.mu.Unlock()

		mergedChunk.Write(peak.res.Data)

		d.chunk.heap.Pop()
		proto.PiecePool.Put(peak.res)
	}

	err := d.writeChunkToDist(start, mergedChunk.B)
	if err != nil {
		return
	}

	// Mark only the blocks that were actually written as finished.
	// track which pieces were fully completed.
	var completedPieces []uint32
	for pi := headPi; pi <= tailPi; pi++ {
		pieceIdx := pi / d.normalChunkLen
		blockIdx := int(pi % d.normalChunkLen)
		d.picker.markAsFinished(pieceIdx, blockIdx)

		if d.checkPieceBitmapDone(pieceIdx) {
			// avoid duplicates
			already := false
			for _, cp := range completedPieces {
				if cp == pieceIdx {
					already = true
					break
				}
			}
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

func (d *Download) handleResEndgame(res *proto.ChunkResponse) {
	d.chunk.heap.Push(responseChunk{
		res: res,
		pi:  res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen,
	})

	for d.chunk.heap.Len() != 0 {
		chunk := d.chunk.heap.Pop()
		index := chunk.res.PieceIndex
		err := d.writeChunkToDist(int64(index)*d.info.PieceLength+int64(chunk.res.Begin), chunk.res.Data)
		d.chunk.mu.Lock()
		d.chunk.done.Set(chunk.pi)
		d.chunk.mu.Unlock()
		proto.PiecePool.Put(chunk.res)

		// Mark block as finished in the picker
		blockIdx := int(chunk.res.Begin / uint32(defaultBlockSize))
		d.picker.markAsFinished(index, blockIdx)

		if err != nil {
			continue
		}

		if d.checkPieceBitmapDone(index) {
			tasks.Submit(func() {
				err := d.checkPiece(index)
				if err != nil {
					return
				}

				d.checkDone()
			})
		}
	}
}

// find all chunks from chunkHeap and write them to disk.
func (d *Download) handlePieceFromHeap(index uint32) {
	chunks := heap.New[responseChunk]()
	for _, chunk := range d.chunk.heap.Data {
		if chunk.res.PieceIndex == index {
			chunks.Push(chunk)
		}
	}

	if chunks.Len() != int(pieceChunksCount(d.info, index)) {
		return
	}

	for _, chunk := range chunks.Data {
		d.chunk.heap.Data = gslice.Remove(d.chunk.heap.Data, chunk)
	}

	buf := mempool.GetWithCap(int(d.pieceLength(index)))
	defer mempool.Put(buf)
	buf.Reset()

	for chunks.Len() != 0 {
		chunk := chunks.Pop()
		buf.Write(chunk.res.Data)
		d.chunk.mu.Lock()
		d.chunk.done.Set(chunk.pi)
		d.chunk.mu.Unlock()
		proto.PiecePool.Put(chunk.res)
	}

	err := d.writeChunkToDist(int64(index)*d.info.PieceLength, buf.B)
	if err != nil {
		return
	}

	// Mark all blocks as finished in the picker
	for bi := range int(pieceChunksCount(d.info, index)) {
		d.picker.markAsFinished(index, bi)
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
	pieceCidEnd := pieceCidStart + uint32(pieceChunksCount(d.info, index))

	d.chunk.mu.RLock()
	defer d.chunk.mu.RUnlock()

	for i := pieceCidStart; i < pieceCidEnd; i++ {
		if !d.chunk.done.Contains(i) {
			return false
		}
	}

	return true
}

func (d *Download) writeChunkToDist(begin int64, data []byte) error {
	size := int64(len(data))

	var offset int64
	for _, chunk := range fileChunks(d.info, begin, begin+size) {
		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			d.setError(err)
			return errgo.Wrap(err, "failed to open file for writing chunk")
		}

		_, err = f.File.WriteAt(data[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			f.Close()
			d.setError(err)
			return errgo.Wrap(err, "failed to write chunk")
		}

		f.Release()
		offset += chunk.length
	}

	return nil
}

func (d *Download) checkPiece(pieceIndex uint32) error {
	// stream hash to avoid buffering very large pieces in memory
	const hashBufSize = 1 << 20 // 1 MiB cap per read

	pieceSize := d.pieceLength(pieceIndex)
	bufSize := int(min(int64(hashBufSize), pieceSize))
	if bufSize == 0 {
		bufSize = sha1.Size
	}

	buf := mempool.GetWithCap(bufSize)
	defer mempool.Put(buf)

	hasher := sha1.New()
	piece := d.pieceInfo.fileChunks(pieceIndex)

	for _, chunk := range piece {
		f, err := d.openFileReadOnly(chunk.fileIndex)
		if err != nil {
			return errgo.Wrap(err, "failed to open file for hashing")
		}

		remaining := chunk.length
		offset := chunk.offsetOfFile
		for remaining > 0 {
			toRead := int(min(int64(len(buf.B)), remaining))

			n, err := f.File.ReadAt(buf.B[:toRead], offset)
			if err != nil && err != io.EOF {
				f.Release()
				return errgo.Wrap(err, "failed to read piece data for hashing")
			}

			if n == 0 {
				f.Release()
				return errgo.Wrap(io.ErrUnexpectedEOF, "failed to read piece data for hashing")
			}

			hasher.Write(buf.B[:n])
			remaining -= int64(n)
			offset += int64(n)
		}

		f.Release()
	}

	var digest [sha1.Size]byte
	copy(digest[:], hasher.Sum(nil))

	if digest != d.info.Pieces[pieceIndex] {
		// Piece hash check failed — reset in picker so blocks can be re-requested
		d.picker.resetPiece(pieceIndex, d.info)
		d.corruptedPiecesMu.Lock()
		d.corruptedPieces[pieceIndex]++
		d.corruptedPiecesMu.Unlock()
		d.corrupted.Add(pieceSize)
		start := pieceIndex * d.normalChunkLen
		end := start + uint32(pieceChunksCount(d.info, pieceIndex))
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

	notHave := d.bm.SetX(pieceIndex)

	// Mark piece as fully owned in the picker
	d.picker.weHave(pieceIndex)

	if notHave {
		d.completed.Add(pieceSize)
		d.corruptedPiecesMu.Lock()
		delete(d.corruptedPieces, pieceIndex)
		d.corruptedPiecesMu.Unlock()
		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)

		// Signal peers that a piece completed so they can request more blocks.
		d.peers.Range(func(_ netip.AddrPort, p *Peer) bool {
			select {
			case p.pieceDone <- empty.Empty{}:
			default:
			}
			return true
		})
	}

	return nil
}

func (d *Download) checkDone() {
	if d.bm.Count() != d.info.NumPieces {
		return
	}

	if err := d.transition(Seeding); err != nil {
		d.log.Error().Err(err).Msg("failed to transition state in checkDone")
		return
	}
	d.CompletedAt.Store(time.Now().Unix())
	d.pieceDownloadRate.Reset()

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.Bitmap.Count() == d.info.NumPieces {
			p.close()
		}

		return true
	})

	d.Trk.Announce(tracker.EventCompleted)
}

// requestABlock picks blocks for a single peer using the global piece picker.
// Mirrors libtorrent's request_a_block().
//
// It determines the desired queue size dynamically (rate-based or snubbed=1),
// calls the piece picker to find blocks, filters out already-queued blocks,
// and pushes free blocks into the peer's request channel. In endgame mode,
// it may also pick busy blocks (already requested by other peers).
func (d *Download) requestABlock(p *Peer) {
	if d.bm.Count() == d.info.NumPieces {
		return // we're a seed, nothing to request
	}
	if p.closed.Load() || p.isDisconnecting() {
		return
	}
	if !d.HasState(Downloading) {
		return
	}


	// Determine desired queue size
	desiredQueueSize := p.updateDesiredQueueSize()

	// Already have enough outstanding requests
	numRequests := desiredQueueSize - p.myRequests.Size() - p.requestQueueLen()
	if numRequests <= 0 {
		return
	}

	// Check if we should enter endgame mode
	remaining := d.SelectedSize() - d.completed.Load()
	if remaining <= units.MiB*100 {
		d.endGameMode.Store(true)
	}

	// Build bitfield of pieces peer has
	choked := p.peerChoking.Load()

	// Call the piece picker
	result := d.picker.pickPieces(
		p.Bitmap,
		choked,
		p.allowFast,
		numRequests,
		0, // prefer_contiguous_blocks (can be enabled for fast peers later)
		nil, // suggested pieces (not yet implemented)
		d.info,
	)

	// Pick free blocks first
	freeBlocksPicked := 0
	for _, fb := range result.freeBlocks {
		if numRequests <= 0 {
			break
		}

		// Check if the block is already in the peer's queues
		chunk := pieceChunk(d.info, fb.pieceIndex, fb.blockIndex)
		if p.isInQueue(chunk) {
			continue
		}

		// Check if block is already finished
		if d.picker.isFinished(fb.pieceIndex, fb.blockIndex) {
			continue
		}

		// Check if piece is already downloaded
		if d.bm.Contains(fb.pieceIndex) {
			continue
		}

		// Check if chunk is already written to disk
		chunkPi := fb.pieceIndex*d.normalChunkLen + uint32(fb.blockIndex)
		d.chunk.mu.RLock()
		done := d.chunk.done.Contains(chunkPi)
		d.chunk.mu.RUnlock()
		if done {
			continue
		}

		// Mark block as requesting in the picker
		d.picker.markAsRequesting(fb.pieceIndex, fb.blockIndex, p)
		d.picker.addDownloadingPiece(fb.pieceIndex, d.info)

		// Push block to peer's request queue
		select {
		case p.blockRequests <- pieceBlock{pieceIndex: fb.pieceIndex, blockIndex: fb.blockIndex}:
			numRequests--
			freeBlocksPicked++
		default:
			// peer's request queue full, abort the mark
			d.picker.abortDownload(fb.pieceIndex, fb.blockIndex)
			return
		}
	}

	// If we could pick enough free blocks, we're not in endgame for this peer
	if numRequests <= 0 {
		p.setEndgame(false)
		return
	}

	// If we couldn't find enough free blocks, this peer enters endgame.
	// Only set endgame if we're not choked (choked means only allowed-fast,
	// which doesn't count as endgame).
	if !p.peerChoking.Load() {
		p.setEndgame(true)
		d.endGameMode.Store(true)
	}

	// In endgame, also try busy blocks, but only if we have
	// no outstanding requests already (to avoid all peers
	// fighting over the same blocks).
	// Mirrors libtorrent: dq.size() + rq.size() > 0 check.
	if !d.endGameMode.Load() || p.myRequests.Size()+p.requestQueueLen() > 0 {
		return
	}

	if len(result.busyBlocks) > 0 {

		for _, bb := range result.busyBlocks {
			chunk := pieceChunk(d.info, bb.pieceIndex, bb.blockIndex)
			if p.isInQueue(chunk) {
				continue
			}

			if d.picker.isFinished(bb.pieceIndex, bb.blockIndex) {
				continue
			}

			if d.bm.Contains(bb.pieceIndex) {
				continue
			}

			chunkPi := bb.pieceIndex*d.normalChunkLen + uint32(bb.blockIndex)
			d.chunk.mu.RLock()
			done := d.chunk.done.Contains(chunkPi)
			d.chunk.mu.RUnlock()
			if done {
				continue
			}

			d.picker.markAsRequesting(bb.pieceIndex, bb.blockIndex, p)
			d.picker.addDownloadingPiece(bb.pieceIndex, d.info)

			select {
			case p.blockRequests <- pieceBlock{pieceIndex: bb.pieceIndex, blockIndex: bb.blockIndex}:
			default:
				d.picker.abortDownload(bb.pieceIndex, bb.blockIndex)
			}
			return
		}
	}
}

func pieceChunksCount(info meta.Info, index uint32) int64 {
	pieceSize := info.PieceLength
	if index == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}

	return (pieceSize + defaultBlockSize - 1) / defaultBlockSize
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
