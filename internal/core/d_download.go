// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"cmp"
	"crypto/sha1"
	"io"
	"net/netip"
	"slices"
	"time"

	"github.com/docker/go-units"
	"github.com/trim21/errgo"

	"neptune/internal/core/tracker"
	"neptune/internal/meta"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/pkg/gslice"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

func (d *Download) backgroundReqScheduler() {
	timer := time.NewTimer(time.Second * 5)
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleRequestSignal:
			if !d.wait(Downloading) {
				continue
			}

			d.scheduleSeq()
		case <-timer.C:
			if !d.wait(Downloading) {
				continue
			}

			d.scheduleSeq()
		}
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

	// clear endgame tracking for this chunk
	if d.endGameMode.Load() {
		d.endgameRequested.Delete(res.Request())
	}

	// in endgame mode we may receive duplicated response, just ignore them
	if d.bm.Contains(res.PieceIndex) {
		return
	}

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
			if !d.chunk.pending.Contains(c.pi) {
				return
			}
		}
		d.handlePieceFromHeap(res.PieceIndex)
		return
	}

	head := d.chunk.heap.Pop()
	headPi := head.pi

	mergedChunk := pieceChunksPool.Get()
	defer pieceChunksPool.Put(mergedChunk)
	mergedChunk.Reset()

	d.chunk.mu.Lock()
	d.chunk.done.Set(headPi)
	d.chunk.mu.Unlock()

	headPiece := head.res.PieceIndex
	tailPiece := headPiece
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
		tailPiece = peak.res.PieceIndex

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

	for pieceIndex := headPiece; pieceIndex <= tailPiece; pieceIndex++ {
		if !d.checkPieceBitmapDone(pieceIndex) {
			continue
		}

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
		d.chunk.done.Set(chunk.pi)
		proto.PiecePool.Put(chunk.res)

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
		return nil
	}

	notHave := d.bm.SetX(pieceIndex)

	if notHave {
		d.completed.Add(pieceSize)
		d.corruptedPiecesMu.Lock()
		delete(d.corruptedPieces, pieceIndex)
		d.corruptedPiecesMu.Unlock()
		d.piecePeerMu.Lock()
		delete(d.piecePeerAssignments, pieceIndex)
		d.piecePeerMu.Unlock()
		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)
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

// piecePriority computes a priority score for a piece, inspired by libtorrent:
// priority = (availability + 1) * 3 + state_adjustment
// Higher priority means more urgent to download.
func piecePriority(availability int32, hasPendingChunks bool) int32 {
	p := (availability + 1) * 3
	if hasPendingChunks {
		p += 1 // boost partially downloaded pieces
	}
	return p
}

func (d *Download) updateRarePieces() {
	d.ratePieceMutex.Lock()
	defer d.ratePieceMutex.Unlock()

	if len(d.pieceAvailability) == 0 {
		d.pieceAvailability = make([]int32, d.info.NumPieces)
	} else {
		clear(d.pieceAvailability)
	}

	var baseAvail int32
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.peerChoking.Load() {
			p.allowFast.Range(func(u uint32) {
				if p.Bitmap.Contains(u) {
					d.pieceAvailability[u]++
				}
			})
			return true
		}

		if p.Bitmap.Count() == d.info.NumPieces {
			baseAvail++
			return true
		}

		p.Bitmap.Range(func(u uint32) {
			d.pieceAvailability[u]++
		})

		return true
	})

	var queue = make([]pieceRare, 0, d.info.NumPieces)

	for index, avail := range d.pieceAvailability {
		if d.bm.Contains(uint32(index)) {
			continue
		}

		totalAvail := avail + baseAvail
		if totalAvail == 0 {
			continue
		}

		if !d.selectedPiecesBm.Contains(uint32(index)) {
			continue
		}

		// check if piece has pending chunks (partially downloaded)
		pieceStart := uint32(index) * d.normalChunkLen
		pieceEnd := pieceStart + uint32(pieceChunksCount(d.info, uint32(index)))
		hasPending := false
		d.chunk.mu.RLock()
		for c := pieceStart; c < pieceEnd; c++ {
			if d.chunk.done.Contains(c) {
				hasPending = true
				break
			}
		}
		d.chunk.mu.RUnlock()

		priority := piecePriority(totalAvail, hasPending)
		if priority > 0 {
			queue = append(queue, pieceRare{priority: priority, index: uint32(index)})
		}
	}

	d.rarePieceQueue = heap.FromSlice(queue)
}

func (d *Download) scheduleSeq() {
	if d.endGameMode.Load() {
		d.scheduleSeqEndGame()
		return
	}

	if d.SelectedSize()-d.completed.Load() <= units.MiB*100 {
		d.endGameMode.Store(true)
		d.scheduleSeqEndGame()
		return
	}

	// Phase 1: prioritize partial pieces (already partially downloaded)
	d.schedulePartialPieces()

	// Phase 2: rarest-first for remaining capacity
	d.scheduleRarePieces()
}

// schedulePartialPieces assigns pieces that are already partially downloaded.
// This reduces memory usage and completes work-in-progress faster.
func (d *Download) schedulePartialPieces() {
	type partialPiece struct {
		index    uint32
		progress int // number of chunks already downloaded
	}

	var partials []partialPiece

	for i := range d.info.NumPieces {
		if d.bm.Contains(i) {
			continue
		}
		if !d.selectedPiecesBm.Contains(i) {
			continue
		}

		pieceStart := i * d.normalChunkLen
		pieceEnd := pieceStart + uint32(pieceChunksCount(d.info, i))
		downloaded := 0
		d.chunk.mu.RLock()
		for c := pieceStart; c < pieceEnd; c++ {
			if d.chunk.done.Contains(c) {
				downloaded++
			}
		}
		d.chunk.mu.RUnlock()

		if downloaded > 0 {
			partials = append(partials, partialPiece{index: i, progress: downloaded})
		}
	}

	if len(partials) == 0 {
		return
	}

	// sort by progress descending (almost-done pieces first)
	slices.SortFunc(partials, func(a, b partialPiece) int {
		return cmp.Compare(b.progress, a.progress)
	})

	d.assignPiecesToPeers(func() (uint32, bool) {
		if len(partials) == 0 {
			return 0, false
		}
		p := partials[0]
		partials = partials[1:]
		return p.index, true
	})
}

// scheduleRarePieces assigns pieces using rarest-first strategy.
func (d *Download) scheduleRarePieces() {
	d.updateRarePieces()

	d.ratePieceMutex.Lock()
	q := d.rarePieceQueue
	d.rarePieceQueue = heap.New[pieceRare]()
	d.ratePieceMutex.Unlock()

	d.assignPiecesToPeers(func() (uint32, bool) {
		if q.Len() == 0 {
			return 0, false
		}
		piece := q.Pop()
		if piece.priority <= 0 {
			return 0, false
		}
		return piece.index, true
	})
}

// assignPiecesToPeers assigns pieces from the iterator to peers.
// Peers are sorted by speed (fastest first) and get pieces proportional to their pipeline capacity.
func (d *Download) assignPiecesToPeers(nextPiece func() (uint32, bool)) {
	type peerInfo struct {
		peer     *Peer
		capacity int // how many more pieces this peer can handle
	}

	var peers []peerInfo
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.closed.Load() {
			return true
		}
		queueSize := int(p.desiredQueueSize.Load())
		pending := p.myRequests.Size()
		// rough estimate: each piece has multiple chunks, divide by avg chunks
		pendingPieces := 0
		if pending > 0 {
			pendingPieces = max(pending/int(d.normalChunkLen), 1)
		}
		avail := queueSize - pendingPieces
		if avail > 0 {
			peers = append(peers, peerInfo{peer: p, capacity: avail})
		}
		return true
	})

	if len(peers) == 0 {
		return
	}

	// sort by download rate descending (fastest peer first)
	slices.SortFunc(peers, func(a, b peerInfo) int {
		return cmp.Compare(b.peer.pieceDownloadRate.Status().CurRate, a.peer.pieceDownloadRate.Status().CurRate)
	})

	for _, pi := range peers {
		for pi.capacity > 0 {
			pieceIndex, ok := nextPiece()
			if !ok {
				return
			}

			if !d.selectedPiecesBm.Contains(pieceIndex) {
				continue
			}

			if pi.peer.peerChoking.Load() {
				if !pi.peer.allowFast.Contains(pieceIndex) {
					continue
				}
			} else if !pi.peer.Bitmap.Contains(pieceIndex) {
				continue
			}

			// skip peers that previously sent corrupted data for this piece
			d.piecePeerMu.Lock()
			if peers, ok := d.piecePeerAssignments[pieceIndex]; ok {
				if _, bad := peers[pi.peer.id]; bad {
					d.piecePeerMu.Unlock()
					continue
				}
			}
			// record this assignment
			if d.piecePeerAssignments[pieceIndex] == nil {
				d.piecePeerAssignments[pieceIndex] = make(map[uint32]struct{})
			}
			d.piecePeerAssignments[pieceIndex][pi.peer.id] = struct{}{}
			d.piecePeerMu.Unlock()

			select {
			case pi.peer.ourPieceRequests <- pieceIndex:
				pi.peer.Requested.Set(pieceIndex)
				pi.capacity--
			default:
				// channel full, move to next peer
				break
			}
		}
	}
}

func (d *Download) scheduleSeqEndGame() {
	missing := bm.New(d.info.NumPieces)
	missing.Fill()
	missing.AndNot(d.bm)
	s := missing.ToArray()

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		for _, u := range s {
			if !d.selectedPiecesBm.Contains(u) {
				continue
			}
			if p.Bitmap.Contains(u) {
				// check if this peer has any unrequested chunks for this piece
				if !d.hasUnrequestedEndgameChunks(u) {
					continue
				}
				select {
				case <-p.ctx.Done():
					return true
				case p.ourPieceRequests <- u:
				default:
				}
			}
		}

		return true
	})
}

// hasUnrequestedEndgameChunks returns true if the piece has chunks not yet requested in endgame mode.
func (d *Download) hasUnrequestedEndgameChunks(pieceIndex uint32) bool {
	if !d.endGameMode.Load() {
		return true
	}
	pieceStart := pieceIndex * d.normalChunkLen
	pieceEnd := pieceStart + uint32(pieceChunksCount(d.info, pieceIndex))
	d.chunk.mu.RLock()
	defer d.chunk.mu.RUnlock()
	for c := pieceStart; c < pieceEnd; c++ {
		if !d.chunk.done.Contains(c) {
			req := pieceChunk(d.info, pieceIndex, int(c-pieceStart))
			if _, requested := d.endgameRequested.Load(req); !requested {
				return true
			}
		}
	}
	return false
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
