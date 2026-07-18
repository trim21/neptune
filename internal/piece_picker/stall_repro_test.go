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
	respondPieceForTest(t, pp, 1, 1)
	pp.missingBm.Unset(1)
	pp.WeHave(1)

	// Piece 0 fully received, all blocks responded, hash check pending.
	respondPieceForTest(t, pp, 0, 2)
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

	peerBitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 100, 0, nil, false, 0, result)

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

	peerBitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	peerBitfield.Fill()

	// Add piece 0 to downloadingPieces with some free blocks.
	peer := newPickerTestPeer(t, pp, 1)
	claims := peer.pickPiece(0, 2)
	if len(claims) != 2 {
		t.Fatalf("claimed %d blocks for piece 0, want 2", len(claims))
	}
	pp.AddDownloadingPiece(0)

	// Simulate the race: SetX runs but weHave hasn't removed from downloadingPieces yet.
	pp.missingBm.Unset(0)

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, false, 0, result)

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
	respondPieceForTest(t, oldPicker, 0, 1)
	oldPicker.AddDownloadingPiece(0)
	oldPicker.missingBm.Unset(0)

	// Create NEW picker.
	newPicker := newTestPicker(10, 4)
	newPicker.missingBm = oldPicker.missingBm // share missingBm
	respondPieceForTest(t, newPicker, 1, 2)
	newPicker.missingBm.Unset(1)
	newPicker.WeHave(1)
	newPicker.WeHave(0) // piece 0 not in newPicker's downloadingPieces → no-op

	// OLD picker still has piece 0 in downloadingPieces, all blocks responded.
	peerBitfield := bm.NewLockFreeBitmap(oldPicker.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = oldPicker.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(oldPicker.info.NumPieces), 100, 0, nil, false, 0, result)

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
	respondPieceForTest(t, pp, 0, 1)
	pp.missingBm.Unset(0)
	pp.WeHave(0)

	// Piece 1: 0 free, 1 requested, 3 responded (stalled near completion).
	peer := newPickerTestPeer(t, pp, 2)
	claims := peer.pickPiece(1, 4)
	if len(claims) != 4 {
		t.Fatalf("claimed %d blocks for piece 1, want 4", len(claims))
	}
	for _, claim := range claims[1:] {
		if !peer.accept(claim) {
			t.Fatalf("failed to accept claim %+v", claim.Block)
		}
	}

	// Piece 2: complete it so numWantLeft goes to 0 (triggers endgame).
	respondPieceForTest(t, pp, 2, 3)
	pp.missingBm.Unset(2)
	pp.WeHave(2)

	peerBitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 100, 0, nil, false, 0, result)

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

	peerBitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	peerBitfield.Fill()
	peer := newPickerTestPeer(t, pp, 1)
	pick := func(numBlocks int) []BlockClaim {
		return peer.pick(PickRequest{
			Bitfield:      peerBitfield,
			BlockedPieces: bm.NewLockFreeBitmap(pp.info.NumPieces),
			NumBlocks:     numBlocks,
		})
	}

	// Sequential: should always pick block 0 of piece 0 first.
	claims := pick(1)
	if len(claims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(claims))
	}
	if claims[0].Block.PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", claims[0].Block.PieceIndex)
	}
	if claims[0].Block.BlockIndex != 0 {
		t.Fatalf("expected block 0, got %d", claims[0].Block.BlockIndex)
	}

	// Next request: should be block 1 of piece 0 (still sequential).
	second := pick(1)
	if len(second) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(second))
	}
	if second[0].Block.PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", second[0].Block.PieceIndex)
	}
	if second[0].Block.BlockIndex != 1 {
		t.Fatalf("expected block 1 of piece 0, got block %d", second[0].Block.BlockIndex)
	}
	claims = append(claims, second...)

	claims = append(claims, peer.pickPiece(0, 8)...)
	if len(claims) != 10 {
		t.Fatalf("claimed %d blocks for piece 0, want 10", len(claims))
	}
	for _, claim := range claims {
		if !peer.accept(claim) {
			t.Fatalf("failed to accept claim %+v", claim.Block)
		}
	}
	pp.WeHave(0)

	// Next request: piece 1, block 0.
	next := pick(1)
	if len(next) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(next))
	}
	if next[0].Block.PieceIndex != 1 {
		t.Fatalf("expected piece 1, got %d", next[0].Block.PieceIndex)
	}
}
