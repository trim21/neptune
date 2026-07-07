// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"sync"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

// blockState represents the state of an individual block within a piece.
type blockState uint8

const (
	blockStateNone      blockState = iota // block is free, not yet requested
	blockStateRequested                   // block is currently requested from a peer
	blockStateWriting                     // block data is being written to disk
	blockStateFinished                    // block has been written to disk
)

// blockInfo tracks per-block metadata: which peer requested it, how many peers
// have it in their queues, and the current state.
type blockInfo struct {
	// peer that requested this block (nil if none)
	peer *Peer
	// number of peers that have this block in their download or request queue
	numPeers uint16
	// current state of this block
	state blockState
}

// downloadingPiece represents a piece that is partially downloaded.
type downloadingPiece struct {
	infoIdx         int
	index           uint32
	blocksInPiece   uint16
	finished        uint16
	writing         uint16
	requested       uint16
	passedHashCheck bool
	locked          bool
}

// piecePriority computes a score for a piece.
// Higher score = more urgent. Mirrors libtorrent: (availability + 1) * priority_factor.
const priorityFactor = 3

// piecePicker is a global block-level piece picker, mirroring libtorrent's
// piece_picker. It centralizes tracking of which blocks are requested/writing/
// finished, and provides block-level selection for peers.
//
// All public methods are safe for concurrent use.
type piecePicker struct {
	completedBm        *bm.Bitmap
	availability       []uint16
	pieces             []uint32
	piecePriorities    []uint32
	downloadingPieces  []downloadingPiece
	blockInfos         []blockInfo
	blockSize          int64
	downloadQueueSize  int
	numWantLeft        int
	mu                 sync.Mutex
	numCompletedPieces uint32
	numPieces          uint32
	blocksPerPiece     uint32
	dirty              bool
}

// newPiecePicker creates a new piece picker for the given torrent info.
// completedBm is the Download's completed bitmap — the picker reads it directly
// instead of maintaining its own copy.
func newPiecePicker(info meta.Info, completedBm *bm.Bitmap) *piecePicker {
	numPieces := info.NumPieces
	blocksPerPiece := uint32((info.PieceLength + defaultBlockSize - 1) / defaultBlockSize)

	pp := &piecePicker{
		numPieces:       numPieces,
		blocksPerPiece:  blocksPerPiece,
		blockSize:       defaultBlockSize,
		availability:    make([]uint16, numPieces),
		piecePriorities: make([]uint32, numPieces),
		pieces:          make([]uint32, numPieces),
		completedBm:     completedBm,
		dirty:           true,
		blockInfos:      make([]blockInfo, int(numPieces)*int(blocksPerPiece)),
	}

	// initialize pieces array
	for i := range numPieces {
		pp.pieces[i] = i
		pp.piecePriorities[i] = 1 * priorityFactor // initial priority = (0+1)*3
	}

	// initialize block info array: set piece_index for each block
	// (for debugging only — removed in production build)

	return pp
}

// numBlocksInPiece returns the number of blocks in the given piece.
func (pp *piecePicker) numBlocksInPiece(info meta.Info, pieceIndex uint32) uint16 {
	pieceSize := info.PieceLength
	if pieceIndex == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}
	return uint16((pieceSize + defaultBlockSize - 1) / defaultBlockSize)
}

// blockInfoIdx returns the starting index in blockInfos for the given piece.
func (pp *piecePicker) blockInfoIdx(pieceIndex uint32) int {
	return int(pieceIndex) * int(pp.blocksPerPiece)
}

// incRefcount increments the reference count for all blocks in the given piece.
// Called when a peer acquires a piece (bitfield/have).
func (pp *piecePicker) incRefcount(pieceIndex uint32) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.availability[pieceIndex]++
	if pp.availability[pieceIndex] == 1 {
		// piece went from unavailable to available
		pp.dirty = true
	}
}

// decRefcount decrements the reference count for all blocks in the given piece.
// Called when a peer loses a piece (disconnect, dont_have).
func (pp *piecePicker) decRefcount(pieceIndex uint32) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if pp.availability[pieceIndex] > 0 {
		pp.availability[pieceIndex]--
	}
	if pp.availability[pieceIndex] == 0 {
		pp.dirty = true
	}
}

// weHave marks a piece as completed (we now have it).
// It clears all block states for that piece and increments the completed counter.
func (pp *piecePicker) weHave(pieceIndex uint32, info meta.Info) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(info, pieceIndex)
	for i := range int(nb) {
		bi := &pp.blockInfos[idx+i]
		if bi.state == blockStateRequested {
			pp.downloadQueueSize--
		}
		bi.state = blockStateFinished
		bi.peer = nil
		bi.numPeers = 0
	}
	pp.removeDownloadingPieceLocked(pieceIndex)
	pp.numCompletedPieces++
	pp.dirty = true
}

// markAsWriting marks a block as being written to disk.
func (pp *piecePicker) markAsWriting(pieceIndex uint32, blockIndex int) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	bi := &pp.blockInfos[idx]
	oldState := bi.state
	if bi.state == blockStateRequested {
		pp.downloadQueueSize--
	}
	bi.state = blockStateWriting
	bi.peer = nil

	// Update downloadingPiece counters
	if dp := pp.findDownloadingPiece(pieceIndex); dp != nil {
		if oldState == blockStateRequested {
			dp.requested--
		}
		dp.writing++
	}
}

// markAsFinished marks a block as having been written to disk.
func (pp *piecePicker) markAsFinished(pieceIndex uint32, blockIndex int) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	bi := &pp.blockInfos[idx]
	oldState := bi.state
	bi.state = blockStateFinished
	bi.peer = nil

	// Update downloadingPiece counters
	if dp := pp.findDownloadingPiece(pieceIndex); dp != nil {
		if oldState == blockStateWriting {
			dp.writing--
		}
		dp.finished++
	}
}

// markAsRequesting marks a block as requested from a peer.
func (pp *piecePicker) markAsRequesting(pieceIndex uint32, blockIndex int, peer *Peer) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	bi := &pp.blockInfos[idx]
	if bi.state == blockStateNone {
		pp.downloadQueueSize++
	}
	bi.state = blockStateRequested
	bi.peer = peer
	bi.numPeers++

	// Update downloadingPiece counters
	if dp := pp.findDownloadingPiece(pieceIndex); dp != nil {
		dp.requested++
	}
}

// getNumPeers returns the number of peers requesting the given block.
func (pp *piecePicker) getNumPeers(pieceIndex uint32, blockIndex int) int {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	return int(pp.blockInfos[idx].numPeers)
}

// abortDownload releases a block that was requested but not received.
// Called when a request is canceled or times out.
func (pp *piecePicker) abortDownload(pieceIndex uint32, blockIndex int) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	bi := &pp.blockInfos[idx]
	if bi.state == blockStateRequested {
		pp.downloadQueueSize--
	}
	bi.state = blockStateNone
	bi.peer = nil
	if bi.numPeers > 0 {
		bi.numPeers--
	}
}

// isFinished returns true if the block is finished.
func (pp *piecePicker) isFinished(pieceIndex uint32, blockIndex int) bool {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + blockIndex
	return pp.blockInfos[idx].state == blockStateFinished
}

// rebuildPriorities re-sorts the piece priority array.
// Only fully-completed pieces (all blocks finished) are excluded.
// Partially downloaded pieces stay in the array — pickPieces handles them
// via the partial-pieces phase first, then falls back to the priority list.
func (pp *piecePicker) rebuildPriorities(info meta.Info) {
	if !pp.dirty {
		return
	}

	available := pp.pieces[:0]
	for _, pi := range pp.pieces {
		if pp.completedBm.Contains(pi) || pp.allBlocksFinished(pi, info) {
			continue
		}
		available = append(available, pi)
	}

	// sort by priority descending, then by piece index
	slices.SortFunc(available, func(a, b uint32) int {
		pa := pp.piecePriorities[a]
		pb := pp.piecePriorities[b]
		if pa != pb {
			if pa > pb {
				return -1
			}
			return 1
		}
		if a < b {
			return -1
		}
		return 1
	})

	pp.pieces = available
	pp.numWantLeft = len(available)
	pp.dirty = false
}

// allBlocksFinished returns true if every block of the given piece is finished.
func (pp *piecePicker) allBlocksFinished(pieceIndex uint32, info meta.Info) bool {
	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(info, pieceIndex)
	for i := range int(nb) {
		if pp.blockInfos[idx+i].state != blockStateFinished {
			return false
		}
	}
	return true
}

// updatePiecePriority recalculates the priority for a piece based on its availability.
func (pp *piecePicker) updatePiecePriority(pieceIndex uint32) {
	avail := pp.availability[pieceIndex]
	if avail == 0 {
		avail = 1 // ensure positive priority
	}
	pp.piecePriorities[pieceIndex] = uint32(avail+1) * priorityFactor
}

// pieceBlock represents a specific block (piece index + block index).
type pieceBlock struct {
	pieceIndex uint32
	blockIndex int
}

// pickResult holds the result of pickPieces.
type pickResult struct {
	// Free blocks (not requested by any peer)
	freeBlocks []pieceBlock
	// Busy blocks (already requested by another peer), for endgame fallback.
	busyBlocks []pieceBlock
}

// pickPieces picks blocks for a peer using rarest-first strategy.
//
// Parameters:
//   - bitfield: pieces the peer has
//   - choked: whether the peer has us choked (only allowed fast pieces)
//   - allowedFast: bitmap of allowed fast pieces
//   - numBlocks: desired number of blocks to pick
//   - preferContiguous: >0 means prefer whole pieces, value is the number of contiguous blocks
//   - suggestedPieces: pieces suggested by the peer
//   - info: torrent metadata for block counts
//
// It first prioritizes partial pieces (highest progress first), then uses rarest-first.
func (pp *piecePicker) pickPieces(
	bitfield *bm.Bitmap,
	choked bool,
	allowedFast *bm.Bitmap,
	numBlocks int,
	preferContiguous int,
	suggestedPieces []uint32,
	info meta.Info,
) pickResult {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.rebuildPriorities(info)

	var result pickResult

	// ── Startup mode: no completed pieces and no partial pieces yet ──
	// Pick a random medium-rarity piece to quickly get something to upload.
	if pp.numCompletedPieces == 0 && len(pp.downloadingPieces) == 0 {
		pp.pickStartupBlock(bitfield, choked, allowedFast, &result, info)
		if len(result.freeBlocks) > 0 {
			return result
		}
	}

	// Phase 1: partial pieces (pieces in downloading state with some blocks finished)
	type partialInfo struct {
		pieceIndex uint32
		blockCount int // number of blocks this peer can use from this piece
	}
	var partials []partialInfo

	for _, dp := range pp.downloadingPieces {
		if !bitfield.Contains(dp.index) {
			continue
		}
		if choked && !allowedFast.Contains(dp.index) {
			continue
		}

		idx := pp.blockInfoIdx(dp.index)
		freeBlocks := 0
		for i := range int(dp.blocksInPiece) {
			bi := &pp.blockInfos[idx+i]
			if bi.state == blockStateNone {
				freeBlocks++
			}
		}
		if freeBlocks > 0 {
			partials = append(partials, partialInfo{dp.index, freeBlocks})
		}
	}

	// Sort partials: highest completion ratio first (finish started pieces),
	// then rarest-first on availability as tiebreaker.
	slices.SortFunc(partials, func(a, b partialInfo) int {
		dpA := pp.findDownloadingPiece(a.pieceIndex)
		dpB := pp.findDownloadingPiece(b.pieceIndex)

		// Primary: completion ratio (descending).
		if dpA != nil && dpB != nil {
			rA := float64(dpA.finished) / float64(dpA.blocksInPiece)
			rB := float64(dpB.finished) / float64(dpB.blocksInPiece)
			if rA != rB {
				if rA > rB {
					return -1
				}
				return 1
			}
		}

		// Tiebreaker: rarest-first (higher priority = rarer).
		pa := pp.piecePriorities[a.pieceIndex]
		pb := pp.piecePriorities[b.pieceIndex]
		if pa != pb {
			if pa > pb {
				return -1
			}
			return 1
		}
		return 0
	})

	// Pick from partial pieces first
	for _, p := range partials {
		if numBlocks <= 0 {
			break
		}
		idx := pp.blockInfoIdx(p.pieceIndex)
		dp := pp.findDownloadingPiece(p.pieceIndex)
		if dp == nil {
			continue
		}
		blocksInPiece := int(dp.blocksInPiece)
		for i := range blocksInPiece {
			bi := &pp.blockInfos[idx+i]
			switch bi.state {
			case blockStateNone:
				if preferContiguous <= 0 && numBlocks > 0 {
					result.freeBlocks = append(result.freeBlocks, pieceBlock{p.pieceIndex, i})
					numBlocks--
				}
			case blockStateRequested:
				// busy block — only used in endgame
				if bi.numPeers > 0 {
					result.busyBlocks = append(result.busyBlocks, pieceBlock{p.pieceIndex, i})
				}
			}
		}
	}

	// Phase 2: suggested pieces
	for _, pi := range suggestedPieces {
		if numBlocks <= 0 {
			break
		}
		if !bitfield.Contains(pi) {
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			continue
		}
		pp.pickBlocksFromPiece(pi, info, &numBlocks, &result)
	}

	// Phase 3: rarest-first via greedy scan — pick the best piece each iteration.
	// Avoids sorting openPieces and calling rebuildPriorities per peer.
	for numBlocks > 0 {
		bestPiece := uint32(math.MaxUint32)
		bestPriority := uint32(0)

		for _, pi := range pp.pieces {
			if pp.completedBm.Contains(pi) || pp.allBlocksFinished(pi, info) {
				continue
			}
			if !bitfield.Contains(pi) {
				continue
			}
			if choked && !allowedFast.Contains(pi) {
				continue
			}
			if pp.isAlreadyPicked(pi, &result) {
				continue
			}
			if pp.findDownloadingPiece(pi) != nil {
				continue
			}
			pri := pp.piecePriorities[pi]
			if pri > bestPriority || (pri == bestPriority && pi < bestPiece) {
				bestPriority = pri
				bestPiece = pi
			}
		}

		if bestPiece == math.MaxUint32 {
			break
		}

		pp.pickBlocksFromPiece(bestPiece, info, &numBlocks, &result)
	}

	return result
}

// pickStartupBlock implements the startup mode strategy:
// when we have zero completed pieces, pick a random piece of medium rarity
// (not too rare to avoid stalling, not too common to be worth trading later).
//
// Caller must hold pp.mu.
func (pp *piecePicker) pickStartupBlock(
	bitfield *bm.Bitmap,
	choked bool,
	allowedFast *bm.Bitmap,
	result *pickResult,
	info meta.Info,
) {
	// Collect all pieces the peer has that we want.
	var candidates []uint32
	for _, pi := range pp.pieces {
		if !bitfield.Contains(pi) {
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			continue
		}
		if pp.allBlocksFinished(pi, info) {
			continue
		}
		candidates = append(candidates, pi)
	}
	if len(candidates) == 0 {
		return
	}

	// Sort by availability ascending (rarest first).
	sort.Slice(candidates, func(i, j int) bool {
		return pp.availability[candidates[i]] < pp.availability[candidates[j]]
	})

	// Exclude the top 25% rarest and bottom 25% most common.
	// Pick randomly from the middle 50% to avoid extremes.
	lo := len(candidates) / 4
	hi := len(candidates) * 3 / 4
	if hi <= lo {
		lo = 0
		hi = len(candidates)
	}

	pieceIdx := candidates[lo+rand.IntN(hi-lo)]

	// Pick the first free block from the chosen piece.
	// In startup mode no blocks are in flight, so the first block is always free.
	result.freeBlocks = append(result.freeBlocks, pieceBlock{pieceIdx, 0})
}

// pickBlocksFromPiece picks free blocks from a specific piece.
func (pp *piecePicker) pickBlocksFromPiece(
	pieceIndex uint32,
	info meta.Info,
	numBlocks *int,
	result *pickResult,
) {
	if *numBlocks <= 0 {
		return
	}

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(info, pieceIndex)
	blocksInPiece := int(nb)

	// find free blocks
	for i := range blocksInPiece {
		bi := &pp.blockInfos[idx+i]
		switch bi.state {
		case blockStateNone:
			result.freeBlocks = append(result.freeBlocks, pieceBlock{pieceIndex, i})
			*numBlocks--
			if *numBlocks <= 0 {
				return
			}
		case blockStateRequested:
			result.busyBlocks = append(result.busyBlocks, pieceBlock{pieceIndex, i})
		}
	}
}

// findDownloadingPiece finds a downloading piece by index.
// Caller must hold pp.mu.
func (pp *piecePicker) findDownloadingPiece(pieceIndex uint32) *downloadingPiece {
	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			return &pp.downloadingPieces[i]
		}
	}
	return nil
}

// isAlreadyPicked checks if a piece already has blocks in the result.
// Caller must hold pp.mu.
func (pp *piecePicker) isAlreadyPicked(pieceIndex uint32, result *pickResult) bool {
	for _, fb := range result.freeBlocks {
		if fb.pieceIndex == pieceIndex {
			return true
		}
	}
	return false
}

// addDownloadingPiece adds a piece to the downloading set.
func (pp *piecePicker) addDownloadingPiece(pieceIndex uint32, info meta.Info) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	// check if already added
	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			return
		}
	}

	nb := pp.numBlocksInPiece(info, pieceIndex)
	dp := downloadingPiece{
		index:         pieceIndex,
		infoIdx:       pp.blockInfoIdx(pieceIndex),
		blocksInPiece: nb,
	}

	pp.downloadingPieces = append(pp.downloadingPieces, dp)
	// keep sorted by index for binary search
	slices.SortFunc(pp.downloadingPieces, func(a, b downloadingPiece) int {
		if a.index < b.index {
			return -1
		}
		if a.index > b.index {
			return 1
		}
		return 0
	})
	pp.dirty = true
}

// removeDownloadingPiece removes a piece from the downloading set.
func (pp *piecePicker) removeDownloadingPiece(pieceIndex uint32) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			pp.downloadingPieces = slices.Delete(pp.downloadingPieces, i, i+1)
			pp.dirty = true
			return
		}
	}
}

// countBusyBlocks returns the number of busy (requested) blocks in a piece.
func (pp *piecePicker) countBusyBlocks(pieceIndex uint32, info meta.Info) int {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(info, pieceIndex)
	count := 0
	for i := range int(nb) {
		if pp.blockInfos[idx+i].state == blockStateRequested {
			count++
		}
	}
	return count
}

// PickerStats holds summary block-state counts for debug output.
type PickerStats struct {
	OpenPieces      int
	Downloading     int
	RequestedBlocks int
	WritingBlocks   int
	FinishedBlocks  int
	FreeBlocks      int
	DownloadQueue   int
}

type DownloadingPieceInfo struct {
	Blocks     int
	Finished   int
	Writing    int
	Requested  int
	Free       int
	Index      uint32
	HashPassed bool
	Locked     bool
}

// DebugDownloadingPieces returns detail about every in-flight piece.
func (pp *piecePicker) DebugDownloadingPieces(info meta.Info) []DownloadingPieceInfo {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	result := make([]DownloadingPieceInfo, 0, len(pp.downloadingPieces))
	for _, dp := range pp.downloadingPieces {
		idx := pp.blockInfoIdx(dp.index)
		blocksInPiece := int(dp.blocksInPiece)
		di := DownloadingPieceInfo{
			Index:      dp.index,
			Blocks:     blocksInPiece,
			HashPassed: dp.passedHashCheck,
			Locked:     dp.locked,
		}
		for i := range blocksInPiece {
			switch pp.blockInfos[idx+i].state {
			case blockStateNone:
				di.Free++
			case blockStateRequested:
				di.Requested++
			case blockStateWriting:
				di.Writing++
			case blockStateFinished:
				di.Finished++
			}
		}
		result = append(result, di)
	}
	return result
}

// DebugStats returns picker state summary for debugging.
func (pp *piecePicker) DebugStats(info meta.Info) PickerStats {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	st := PickerStats{
		OpenPieces:    len(pp.pieces),
		Downloading:   len(pp.downloadingPieces),
		DownloadQueue: pp.downloadQueueSize,
	}

	for pi := range pp.numPieces {
		idx := pp.blockInfoIdx(pi)
		nb := pp.numBlocksInPiece(info, pi)
		for i := range int(nb) {
			switch pp.blockInfos[idx+i].state {
			case blockStateNone:
				st.FreeBlocks++
			case blockStateRequested:
				st.RequestedBlocks++
			case blockStateWriting:
				st.WritingBlocks++
			case blockStateFinished:
				st.FinishedBlocks++
			}
		}
	}
	return st
}

// resetPiece resets all blocks in a piece to state none (for hash check failure).
func (pp *piecePicker) resetPiece(pieceIndex uint32, info meta.Info) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(info, pieceIndex)
	for i := range int(nb) {
		bi := &pp.blockInfos[idx+i]
		if bi.state == blockStateRequested {
			pp.downloadQueueSize--
		}
		bi.state = blockStateNone
		bi.peer = nil
		bi.numPeers = 0
	}
	pp.removeDownloadingPieceLocked(pieceIndex)
	pp.dirty = true
}

// resetAll resets all block states to none and clears downloading pieces.
// Used during recheck (AsyncCheck) when completedBm is cleared.
func (pp *piecePicker) resetAll() {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	for i := range pp.blockInfos {
		pp.blockInfos[i].state = blockStateNone
		pp.blockInfos[i].peer = nil
		pp.blockInfos[i].numPeers = 0
	}
	pp.downloadingPieces = pp.downloadingPieces[:0]
	pp.downloadQueueSize = 0
	pp.numCompletedPieces = 0
	pp.dirty = true
}

func (pp *piecePicker) removeDownloadingPieceLocked(pieceIndex uint32) {
	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			pp.downloadingPieces = slices.Delete(pp.downloadingPieces, i, i+1)
			pp.dirty = true
			return
		}
	}
}
