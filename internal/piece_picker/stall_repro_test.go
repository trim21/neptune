// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"neptune/internal/pkg/bm"
)

// TestStallZombiePiece reproduces the "0 free, 0 busy" pattern: A piece has
// all blocks responded (hash check pending), rebuildPriorities removes it
// from pp.pieces, and if it also disappears from downloadingPieces
// (e.g. via picker swap), it becomes unreachable.
func TestStallZombiePiece(t *testing.T) {
	pp := newTestPicker(10, 4)

	// Complete piece 1 to avoid startup mode.
	pp.MarkAsRequesting(1, 0)
	pp.MarkAsResponded(1, 0)
	pp.missingBm.Unset(1)
	pp.WeHave(1)

	// Piece 0 fully received, all blocks responded, hash check pending.
	for bi := range 4 {
		pp.MarkAsRequesting(0, bi)
		pp.MarkAsResponded(0, bi)
	}
	pp.AddDownloadingPiece(0)

	// Manually remove from downloadingPieces (simulating picker swap race).
	pp.mu.Lock()
	for i, dp := range pp.downloadingPieces {
		if dp.index == 0 {
			pp.downloadingPieces = append(pp.downloadingPieces[:i], pp.downloadingPieces[i+1:]...)
			break
		}
	}
	pp.mu.Unlock()

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 100, 0, nil, result)

	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			t.Error("BUG: zombie piece 0 leaked as free block")
		}
	}
}

// TestStallCompletedBmRace reproduces the "compl=2" stall: Phase 1 scans
// downloadingPieces without checking completedBm, returning blocks from
// already-completed pieces. requestABlock rejects them → stall.
func TestStallCompletedBmRace(t *testing.T) {
	pp := newTestPicker(10, 4)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	// Add piece 0 to downloadingPieces with some free blocks.
	pp.MarkAsRequesting(0, 0)
	pp.MarkAsRequesting(0, 1)
	pp.AddDownloadingPiece(0)

	// Simulate the race: SetX runs but weHave hasn't removed from downloadingPieces yet.
	pp.missingBm.Unset(0)

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, result)

	badBlocks := 0
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			badBlocks++
		}
	}
	if badBlocks > 0 {
		t.Errorf("REGRESSION: Phase 1 returned %d free blocks from completed piece 0. "+
			"completedBm check should have filtered them.", badBlocks)
	}
}

// TestStall_NewPickerDoesntInheritDownloadingPieces verifies that when a
// new picker is created, old picker state doesn't leak zombie pieces
// back as free/busy blocks.
func TestStall_NewPickerDoesntInheritDownloadingPieces(t *testing.T) {
	oldPicker := newTestPicker(10, 4)

	// Add piece 0 to OLD picker's downloadingPieces.
	for bi := range 4 {
		oldPicker.MarkAsRequesting(0, bi)
		oldPicker.MarkAsResponded(0, bi)
	}
	oldPicker.AddDownloadingPiece(0)
	oldPicker.missingBm.Unset(0)

	// Create NEW picker.
	newPicker := newTestPicker(10, 4)
	newPicker.missingBm = oldPicker.missingBm // share missingBm
	newPicker.MarkAsRequesting(1, 0)
	newPicker.MarkAsResponded(1, 0)
	newPicker.missingBm.Unset(1)
	newPicker.WeHave(1)
	newPicker.WeHave(0) // piece 0 not in newPicker's downloadingPieces → no-op

	// OLD picker still has piece 0 in downloadingPieces, all blocks responded.
	peerBitfield := bm.New(oldPicker.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = oldPicker.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(oldPicker.info.NumPieces), 100, 0, nil, result)

	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as free block")
		}
	}
	for _, bb := range result.BusyBlocks {
		if bb.PieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as busy block")
		}
	}
}

// TestStallEndgamePicksFreeZeroPieces verifies that in endgame mode
// (numWantLeft==0), busy blocks are collected from downloadingPieces
// even when they have 0 free blocks (e.g. orphaned requested blocks).
func TestStallEndgamePicksFreeZeroPieces(t *testing.T) {
	pp := newTestPicker(3, 4)

	// Complete piece 0 to avoid startup mode.
	pp.MarkAsRequesting(0, 0)
	pp.MarkAsResponded(0, 0)
	pp.missingBm.Unset(0)
	pp.WeHave(0)

	// Piece 1: 0 free, 1 requested, 3 responded (stalled near completion).
	for bi := range 4 {
		pp.MarkAsRequesting(1, bi)
		pp.MarkAsResponded(1, bi)
	}
	pp.MarkAsRequesting(1, 0) // re-mark as requesting (orphan)

	// Piece 2: complete it so numWantLeft goes to 0 (triggers endgame).
	for bi := range 4 {
		pp.MarkAsRequesting(2, bi)
		pp.MarkAsResponded(2, bi)
	}
	pp.missingBm.Unset(2)
	pp.WeHave(2)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 100, 0, nil, result)

	hasBusy := false
	for _, bb := range result.BusyBlocks {
		if bb.PieceIndex == 1 {
			hasBusy = true
		}
	}
	if !hasBusy {
		t.Error("Endgame fallback did not collect busy block from free=0 piece 1")
	}
}

// TestPiecePickStrategy_Sequential verifies that the sequential strategy
// picks blocks in ascending piece order.
func TestPiecePickStrategy_Sequential(t *testing.T) {
	pp := newTestPicker(10, 10)
	pp.strategy.Store(uint32(StrategySequential))

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	var result PickResult

	// Sequential: should always pick block 0 of piece 0 first.
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.FreeBlocks[0].PieceIndex)
	}
	if result.FreeBlocks[0].BlockIndex != 0 {
		t.Fatalf("expected block 0, got %d", result.FreeBlocks[0].BlockIndex)
	}

	// Mark block 0 of piece 0 as requesting.
	pp.MarkAsRequesting(0, 0)
	pp.AddDownloadingPiece(0)

	// Next request: should be block 1 of piece 0 (still sequential).
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.FreeBlocks[0].PieceIndex)
	}
	if result.FreeBlocks[0].BlockIndex != 1 {
		t.Fatalf("expected block 1 of piece 0, got block %d", result.FreeBlocks[0].BlockIndex)
	}

	// Complete piece 0 — mark all blocks as responded.
	for i := range 10 {
		pp.MarkAsResponded(0, i)
	}
	pp.WeHave(0)

	// Next request: piece 1, block 0.
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 1 {
		t.Fatalf("expected piece 1, got %d", result.FreeBlocks[0].PieceIndex)
	}
}
