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
		pp.AddDownloadingPiece(pi)
		for bi := range pp.info.PieceBlockCount(pi) {
			pp.MarkAsRequesting(pi, bi)
			pp.MarkAsResponded(pi, bi)
		}
		pp.completedBm.Set(pi)
		pp.WeHave(pi)
	}

	// Piece 3: all blocks responded (pending hash check).
	pp.AddDownloadingPiece(3)
	for bi := range 4 {
		pp.MarkAsRequesting(3, bi)
		pp.MarkAsResponded(3, bi)
	}

	// Piece 4: 2 requested, 2 responded, 0 free.
	pp.AddDownloadingPiece(4)
	pp.MarkAsRequesting(4, 0)
	pp.MarkAsRequesting(4, 1)
	pp.MarkAsResponded(4, 0)
	pp.MarkAsResponded(4, 1)
	pp.MarkAsRequesting(4, 2)
	pp.MarkAsRequesting(4, 3)

	result := pp.PickPieces(bitfield, false, nil, bm.New(0), 4, 0, nil, PickResult{})

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
