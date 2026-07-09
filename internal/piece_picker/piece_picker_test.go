// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"neptune/internal/pkg/bm"
)

// TestResetPieceIntoCandidates verifies that after a hash failure,
// the piece is re-added to the picker's candidate list and can be picked again.
func TestResetPieceIntoCandidates(t *testing.T) {
	const numPieces = uint32(4)
	const blocksPerPiece = uint32(4)

	pp := newTestPicker(numPieces, blocksPerPiece)

	// Complete piece 1 first (so numCompletedPieces > 0, avoiding startup mode).
	for i := range int(blocksPerPiece) {
		pp.MarkAsRequesting(1, i)
		pp.MarkAsResponded(1, i)
	}
	pp.completedBm.Set(1)
	pp.WeHave(1)

	// Download piece 0: all blocks requested and responded.
	for i := range int(blocksPerPiece) {
		pp.MarkAsRequesting(0, i)
		pp.MarkAsResponded(0, i)
	}

	// Verify piece 0 is NOT pickable (allBlocksResponded, waiting for hash).
	bitfield := bm.New(numPieces)
	bitfield.Fill()
	result := PickResult{}
	result = pp.PickPieces(bitfield, false, nil, 100, 0, nil, result)
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			t.Fatal("piece 0 should not be pickable before resetPiece")
		}
	}

	// Simulate hash failure: resetPiece.
	pp.ResetPiece(0)

	// After resetPiece, piece 0 should be pickable again.
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(bitfield, false, nil, 100, 0, nil, result)

	found := false
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("piece 0 should be pickable after resetPiece")
	}
}
