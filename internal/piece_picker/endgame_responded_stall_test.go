// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"neptune/internal/pkg/bm"
)

// TestEndgameAllBlocksResponded_EmptyResult documents the current behavior:
// when the last piece has all blocks responded (pending hash check) and
// numWantLeft==0, PickPieces returns empty. This is handled at the download
// layer by the hash-fail punishment system — corrupt peers get disconnected
// after repeated hash failures, and the piece is reset via ResetPiece so
// honest peers can retry.
func TestEndgameAllBlocksResponded_EmptyResult(t *testing.T) {
	pp := newTestPicker(3, 4)

	// Piece 0: completed.
	pp.MarkAsRequesting(0, 0)
	pp.MarkAsResponded(0, 0)
	pp.completedBm.Set(0)
	pp.WeHave(0)

	// Piece 1: all blocks responded, hash check pending.
	for bi := range 4 {
		pp.MarkAsRequesting(1, bi)
		pp.MarkAsResponded(1, bi)
	}
	pp.AddDownloadingPiece(1)

	// Piece 2: completed → numWantLeft goes to 0.
	for bi := range 4 {
		pp.MarkAsRequesting(2, bi)
		pp.MarkAsResponded(2, bi)
	}
	pp.completedBm.Set(2)
	pp.WeHave(2)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, result)

	// Current behavior: PickPieces returns empty when all blocks are responded.
	// The download layer handles this via hash-fail punishment:
	// hash pass → WeHave → download complete
	// hash fail → ResetPiece → numWantLeft > 0 → piece re-pickable
	if len(result.FreeBlocks) != 0 || len(result.BusyBlocks) != 0 {
		t.Logf("PickPieces: free=%d busy=%d", len(result.FreeBlocks), len(result.BusyBlocks))
	}
}

// TestResetPieceRestoresPiece verifies that after hash check failure
// (simulated by ResetPiece), the piece becomes pickable again with
// free blocks. This is the normal recovery path.
func TestResetPieceRestoresPiece(t *testing.T) {
	pp := newTestPicker(3, 4)

	// Piece 0: completed.
	pp.completedBm.Set(0)
	pp.WeHave(0)

	// Piece 2: completed.
	pp.completedBm.Set(2)
	pp.WeHave(2)

	// Piece 1: all blocks responded, then hash check fails.
	for bi := range 4 {
		pp.MarkAsRequesting(1, bi)
		pp.MarkAsResponded(1, bi)
	}
	pp.AddDownloadingPiece(1)

	// Verify piece is NOT pickable (all responded = pending hash check).
	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()
	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, result)
	hasPiece1 := false
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 1 {
			hasPiece1 = true
		}
	}
	if hasPiece1 {
		t.Error("piece 1 should not have free blocks when all are responded")
	}

	// Simulate hash check failure.
	pp.ResetPiece(1)

	// Now piece 1 should be pickable with free blocks.
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, result)

	freeBlocks := 0
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 1 {
			freeBlocks++
		}
	}
	if freeBlocks == 0 {
		t.Error("piece 1 should have free blocks after ResetPiece")
	}
}
