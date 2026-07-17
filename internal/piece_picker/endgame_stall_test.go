// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"neptune/internal/pkg/bm"
)

// TestEndgameNoFreeBlocks reproduces the 99% stall where all remaining
// blocks are either requested (from stale/disconnected peers) or responded
// (pending hash check), with zero free blocks. The endgame should return
// busy blocks so the peer can re-request them.
func TestEndgameNoFreeBlocks(t *testing.T) {
	pp := newTestPicker(5, 4)
	bitfield := bm.New(pp.info.NumPieces)
	bitfield.Fill()

	// Complete pieces 0-2.
	for pi := range uint32(3) {
		respondPieceForTest(t, pp, pi, uint64(pi+1))
		pp.missingBm.Unset(pi)
		pp.WeHave(pi)
	}

	// Piece 3: all blocks responded (pending hash check).
	respondPieceForTest(t, pp, 3, 4)

	// Piece 4: 2 requested, 2 responded, 0 free.
	peer := newPickerTestPeer(t, pp, 5)
	claims := peer.pickPiece(4, 4)
	if len(claims) != 4 {
		t.Fatalf("claimed %d blocks for piece 4, want 4", len(claims))
	}
	for _, claim := range claims[:2] {
		if !peer.accept(claim) {
			t.Fatalf("failed to accept claim %+v", claim.Block)
		}
	}

	result := pp.PickPieces(bitfield, false, nil, bm.NewLockFreeBitmap(pp.info.NumPieces), 4, 0, nil, false, 0, PickResult{})

	if len(result.FreeBlocks) == 0 && len(result.BusyBlocks) == 0 {
		t.Error("BUG: pickPieces returned empty when busy blocks exist (99% stall)")
	}

	if len(result.BusyBlocks) > 0 {
		for _, bb := range result.BusyBlocks {
			if bb.PieceIndex < 4 {
				t.Error("busy block from non-target piece")
			}
		}
	}
}
