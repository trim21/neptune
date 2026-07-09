// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// TestStallCompletedBmRace reproduces the "compl=2" stall: Phase 1 scans
// downloadingPieces without checking completedBm, returning blocks from
// already-completed pieces. requestABlock rejects them → stall.
func TestStallCompletedBmRace(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := newPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Add piece 0 to downloadingPieces with some free blocks.
	pp.markAsRequesting(0, 0)
	pp.markAsRequesting(0, 1)
	pp.addDownloadingPiece(0, info)

	// Simulate the race window after checkPiece succeeds:
	// completedBm.SetX runs but weHave hasn't removed from downloadingPieces yet.
	completedBm.SetX(0)

	// pickPieces → Phase 1 finds piece 0 in downloadingPieces, returns free blocks.
	result := pickResult{}
	result = pp.pickPieces(peerBitfield, false, nil, 4, 0, nil, info, StrategyRarestFirst, result)

	badBlocks := 0
	for _, fb := range result.freeBlocks {
		if fb.pieceIndex == 0 {
			badBlocks++
		}
	}
	if badBlocks > 0 {
		t.Errorf("REGRESSION: Phase 1 returned %d free blocks from completed piece 0. "+
			"completedBm check should have filtered them.", badBlocks)
	}
}

// TestStallZombiePiece reproduces the "0 free, 0 busy" pattern:
// A piece has all blocks responded (hash check pending), rebuildPriorities
// removes it from pp.pieces, and if it also disappears from downloadingPieces
// (e.g. via picker swap), it becomes unreachable.
func TestStallZombiePiece(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := newPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Complete piece 1 to avoid startup mode.
	pp.markAsRequesting(1, 0)
	pp.markAsResponded(1, 0)
	completedBm.Set(1)
	pp.weHave(1, info)

	// Simulate: piece 0 fully received, all blocks responded, hash check pending.
	for bi := range int(blocksPerPiece) {
		pp.markAsRequesting(0, bi)
		pp.markAsResponded(0, bi)
	}
	pp.addDownloadingPiece(0, info)

	// Manually remove from downloadingPieces (simulating picker swap race).
	pp.mu.Lock()
	for i, dp := range pp.downloadingPieces {
		if dp.index == 0 {
			pp.downloadingPieces = append(pp.downloadingPieces[:i], pp.downloadingPieces[i+1:]...)
			break
		}
	}
	pp.mu.Unlock()

	// Now: piece 0 NOT in downloadingPieces, NOT in completedBm,
	//      all blocks are blockStateResponded.
	// rebuildPriorities filters it out (allBlocksResponded) → pp.pieces sans 0.

	result := pickResult{}
	result = pp.pickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	for _, fb := range result.freeBlocks {
		if fb.pieceIndex == 0 {
			t.Error("BUG NOT REPRODUCED? Piece 0 still pickable via free blocks")
		}
	}
	for _, bb := range result.busyBlocks {
		if bb.pieceIndex == 0 {
			t.Error("BUG NOT REPRODUCED? Piece 0 still pickable via busy blocks")
		}
	}

	t.Logf("BUG REPRODUCED: zombie piece 0 invisible to picker, total=%d free + %d busy",
		len(result.freeBlocks), len(result.busyBlocks))

	// Recovery: resetPiece should bring piece 0 back.
	pp.resetPiece(0, info)
	result.freeBlocks = result.freeBlocks[:0]
	result.busyBlocks = result.busyBlocks[:0]
	result = pp.pickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	recovered := false
	for _, fb := range result.freeBlocks {
		if fb.pieceIndex == 0 {
			recovered = true
		}
	}
	if !recovered {
		t.Error("resetPiece failed to recover zombie piece 0")
	}
}

// TestStallEndgameBusyLoop tests the full download pipeline near completion
// where the picker spins between compl=2 rejects and 0 free returns.
func TestStallEndgameBusyLoop(t *testing.T) {
	d := newTestDownload(t, 10, 4, piece_store.NewMemStore)

	// Complete pieces 2-9 (80% done).
	for pi := range uint32(10) {
		if pi <= 1 {
			continue
		}
		for bi := range d.info.PieceBlockCount(pi) {
			d.handleRes(&proto.ChunkResponse{
				PieceIndex: pi,
				Begin:      uint32(bi) * defaultBlockSize,
				Data:       make([]byte, defaultBlockSize),
			})
		}
	}
	time.Sleep(20 * time.Millisecond)

	// Only pieces 0 and 1 remain. Start both.
	d.picker.Load().markAsRequesting(0, 0)
	d.picker.Load().addDownloadingPiece(0, d.info)
	d.picker.Load().markAsRequesting(1, 0)
	d.picker.Load().addDownloadingPiece(1, d.info)

	// Receive all blocks for piece 1 → triggers async checkPiece.
	for bi := range d.info.PieceBlockCount(1) {
		if bi == 0 {
			continue
		}
		d.handleRes(&proto.ChunkResponse{
			PieceIndex: 1, Begin: uint32(bi) * defaultBlockSize,
			Data: make([]byte, defaultBlockSize),
		})
	}
	d.handleRes(&proto.ChunkResponse{
		PieceIndex: 1, Begin: 0,
		Data: make([]byte, defaultBlockSize),
	})

	time.Sleep(5 * time.Millisecond) // async checkPiece may be in-flight

	peerBitfield := bm.New(10)
	peerBitfield.Fill()

	pp := d.picker.Load()
	result := pickResult{}
	result = pp.pickPieces(peerBitfield, false, nil, 100, 0, nil, d.info, StrategyRarestFirst, result)

	completedFromResult := 0
	for _, fb := range result.freeBlocks {
		if d.completedBm.Contains(fb.pieceIndex) {
			completedFromResult++
		}
	}
	if completedFromResult > 0 {
		t.Logf("Race hit: %d free blocks from already-completed pieces", completedFromResult)
	}

	d.cancel()
	time.Sleep(10 * time.Millisecond)
}

// FuzzStallNearCompletion fuzzes the picker state space to find scenarios
// where pickPieces returns 0 valid blocks despite remaining pieces.
func FuzzStallNearCompletion(f *testing.F) {
	f.Add(uint32(5), uint32(4), int64(12345))
	f.Add(uint32(10), uint32(4), int64(42))
	f.Add(uint32(8), uint32(6), int64(9999))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed int64) {
		numPieces = clamp(numPieces, 2, 20)
		blocksPerPiece = clamp(blocksPerPiece, 2, 10)

		info := meta.Info{
			NumPieces:     numPieces,
			PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
			LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
		}

		completedBm := bm.New(numPieces)
		wantedBm := bm.New(numPieces)
		wantedBm.Fill()

		pp := newPiecePicker(info, completedBm, wantedBm)
		peerBitfield := bm.New(numPieces)
		peerBitfield.Fill()

		rng := &seededRand{seed: uint64(seed)}

		// Complete piece 0 to avoid startup mode.
		pp.markAsRequesting(0, 0)
		pp.markAsResponded(0, 0)
		completedBm.Set(0)
		pp.weHave(0, info)

		// Randomly assign states to remaining pieces.
		for pi := uint32(1); pi < numPieces; pi++ {
			state := rng.next() % 5
			switch state {
			case 0: // pristine
			case 1: // partially requested
				nReq := int(rng.next()%uint64(blocksPerPiece)) + 1
				for bi := range nReq {
					pp.markAsRequesting(pi, bi)
				}
				pp.addDownloadingPiece(pi, info)
			case 2: // fully responded, in downloadingPieces
				for bi := range int(blocksPerPiece) {
					pp.markAsRequesting(pi, bi)
					pp.markAsResponded(pi, bi)
				}
				pp.addDownloadingPiece(pi, info)
			case 3: // zombie: fully responded, NOT in downloadingPieces
				for bi := range int(blocksPerPiece) {
					pp.markAsRequesting(pi, bi)
					pp.markAsResponded(pi, bi)
				}
				pp.addDownloadingPiece(pi, info)
				pp.mu.Lock()
				for j, dp := range pp.downloadingPieces {
					if dp.index == pi {
						pp.downloadingPieces = append(pp.downloadingPieces[:j], pp.downloadingPieces[j+1:]...)
						break
					}
				}
				pp.mu.Unlock()
			case 4: // race: completedBm set, still in downloadingPieces
				pp.markAsRequesting(pi, 0)
				pp.addDownloadingPiece(pi, info)
				completedBm.SetX(pi)
			}
		}

		var result pickResult
		result = pp.pickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

		// Invariant: Phase 1 now checks completedBm, so no completed-piece blocks
		// should leak into the result.
		badBlocks := 0
		for _, fb := range result.freeBlocks {
			if pp.completedBm.Contains(fb.pieceIndex) {
				badBlocks++
			}
		}
		for _, bb := range result.busyBlocks {
			if pp.completedBm.Contains(bb.pieceIndex) {
				badBlocks++
			}
		}
		if badBlocks > 0 {
			t.Errorf("seed=%d: %d blocks from completed pieces leaked through Phase 1", seed, badBlocks)
		}

		// Invariant: No block from zombie pieces (responded, not in downloadingPieces,
		// not in completedBm — should be excluded by rebuildPriorities).
		zombieBlocks := 0
		pp.mu.Lock()
		for _, fb := range result.freeBlocks {
			if pp.allBlocksResponded(fb.pieceIndex, info) && !pp.completedBm.Contains(fb.pieceIndex) {
				if pp.findDownloadingPiece(fb.pieceIndex) == nil {
					zombieBlocks++
				}
			}
		}
		pp.mu.Unlock()
		if zombieBlocks > 0 {
			t.Errorf("seed=%d: %d zombie blocks leaked (should be filtered)", seed, zombieBlocks)
		}
	})
}

func TestStall_NewPickerDoesntInheritDownloadingPieces(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	oldPicker := newPiecePicker(info, completedBm, wantedBm)

	// Add piece 0 to OLD picker's downloadingPieces.
	for bi := range int(blocksPerPiece) {
		oldPicker.markAsRequesting(0, bi)
		oldPicker.markAsResponded(0, bi)
	}
	oldPicker.addDownloadingPiece(0, info)

	// checkPiece completes: completedBm.SetX(0)
	completedBm.SetX(0)

	// Create NEW picker (strategy change race).
	newPicker := newPiecePicker(info, completedBm, wantedBm)
	newPicker.markAsRequesting(1, 0)
	newPicker.markAsResponded(1, 0)
	completedBm.Set(1)
	newPicker.weHave(1, info)

	// weHave(0) on NEW picker → no-op (piece 0 not in newPicker's downloadingPieces).
	newPicker.weHave(0, info)

	// OLD picker still has piece 0 in downloadingPieces, all blocks responded.
	// If requestABlock loaded OLD picker before the swap → operates on stale state.
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	result := pickResult{}
	result = oldPicker.pickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	for _, fb := range result.freeBlocks {
		if fb.pieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as free block")
		}
	}
	for _, bb := range result.busyBlocks {
		if bb.pieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as busy block")
		}
	}
}

func clamp(v, lo, hi uint32) uint32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// TestStallEndgamePicksFreeZeroPieces verifies that in endgame mode
// (numWantLeft==0), busy blocks are collected from downloadingPieces
// even when they have 0 free blocks (e.g. orphaned requested blocks).
func TestStallEndgamePicksFreeZeroPieces(t *testing.T) {
	const numPieces = uint32(3)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := newPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Complete piece 0 to avoid startup mode.
	pp.markAsRequesting(0, 0)
	pp.markAsResponded(0, 0)
	completedBm.Set(0)
	pp.weHave(0, info)

	// Piece 1: 0 free, 1 requested, 3 responded (stalled near completion).
	for bi := range int(blocksPerPiece) {
		pp.markAsRequesting(1, bi)
		pp.markAsResponded(1, bi)
	}
	// Re-mark block 0 as requesting (simulating orphan).
	pp.markAsRequesting(1, 0)

	// Piece 2: complete it so numWantLeft goes to 0 (triggers endgame).
	for bi := range int(blocksPerPiece) {
		pp.markAsRequesting(2, bi)
		pp.markAsResponded(2, bi)
	}
	completedBm.Set(2)
	pp.weHave(2, info)

	// Now: numWantLeft == 0 (all pieces either completed or in downloadingPieces).
	// Piece 1 has 0 free, 1 requested, 3 responded.
	// Endgame fallback should collect piece 1's requested block as busy.

	result := pickResult{}
	result = pp.pickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	// Should have collected the busy block from piece 1.
	hasBusy := false
	for _, bb := range result.busyBlocks {
		if bb.pieceIndex == 1 {
			hasBusy = true
		}
	}
	if !hasBusy {
		t.Error("Endgame fallback did not collect busy block from free=0 piece 1")
	} else {
		t.Log("Endgame fallback correctly collected busy block from stalled piece 1")
	}
}

var _ = context.Background
