// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"sync"
	"sync/atomic"

	"neptune/internal/meta"
)

// pieceBlocks is the per-block bookkeeping of one in-flight piece: which blocks
// are already written to disk (done) and which are still buffered in the
// chunk heap (pending).
//
// Storing it per in-flight piece keeps memory proportional to the work in
// flight instead of the torrent's total block count. Sized over every block of
// every torrent, the two bitmaps alone reached 300 MiB on a session of 376
// downloading torrents.
type pieceBlocks struct {
	done    []atomic.Uint64
	pending []atomic.Uint64
}

func newPieceBlocks(numBlocks uint32) *pieceBlocks {
	words := int((numBlocks + 63) / 64)

	return &pieceBlocks{
		done:    make([]atomic.Uint64, words),
		pending: make([]atomic.Uint64, words),
	}
}

func (pb *pieceBlocks) setDone(block uint32) {
	pb.done[block/64].Or(1 << (block % 64))
}

func (pb *pieceBlocks) isDone(block uint32) bool {
	return pb.done[block/64].Load()>>(block%64)&1 == 1
}

func (pb *pieceBlocks) setPending(block uint32) {
	pb.pending[block/64].Or(1 << (block % 64))
}

func (pb *pieceBlocks) clearPending(block uint32) {
	pb.pending[block/64].And(^(uint64(1) << (block % 64)))
}

func (pb *pieceBlocks) isPending(block uint32) bool {
	return pb.pending[block/64].Load()>>(block%64)&1 == 1
}

// pieceStates owns the per-block state of every in-flight piece.
//
// The map is guarded by mu; the block words inside pieceBlocks are atomic so
// the verification goroutine can read and clear them without taking the lock,
// matching the previous global bitmaps' concurrency model.
type pieceStates struct {
	m    map[uint32]*pieceBlocks
	info *meta.Info
	mu   sync.Mutex
}

func newPieceStates(info *meta.Info) *pieceStates {
	return &pieceStates{
		info: info,
		m:    make(map[uint32]*pieceBlocks),
	}
}

// peek returns the state of piece without allocating it. A piece that has never
// received a block has no state, which equals "nothing done, nothing pending".
func (s *pieceStates) peek(piece uint32) *pieceBlocks {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.m[piece]
}

// get returns the state of piece, allocating it on first touch.
func (s *pieceStates) get(piece uint32) *pieceBlocks {
	s.mu.Lock()
	defer s.mu.Unlock()

	pb, ok := s.m[piece]
	if !ok {
		pb = newPieceBlocks(uint32(s.info.PieceBlockCount(piece)))
		s.m[piece] = pb
	}

	return pb
}

// release drops the state of a piece whose blocks are accounted for elsewhere:
// it was verified, successfully or not. The piece may be downloaded again
// later, which allocates a fresh state.
func (s *pieceStates) release(piece uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.m, piece)
}

// clear drops every piece state, releasing the memory once the torrent no
// longer downloads.
func (s *pieceStates) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	clear(s.m)
}

// size returns the number of pieces currently tracked.
func (s *pieceStates) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.m)
}

// rangeBlocks calls fn for every tracked piece while holding the map lock.
// Tests use it to assert invariants without racing piece releases.
func (s *pieceStates) rangeBlocks(fn func(piece uint32, pb *pieceBlocks)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for piece, pb := range s.m {
		fn(piece, pb)
	}
}
