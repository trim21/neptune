// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"crypto/sha1"
	"testing"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
)

// TestResetPieceIntoCandidates verifies that after a hash failure,
// the piece is re-added to the picker's candidate list and can be picked again.
// This is the core bug: without the fix, ResetPiece doesn't add the piece back
// to pp.pieces, so PickPieces never selects it for re-download.
func TestResetPieceIntoCandidates(t *testing.T) {
	const numPieces = 4
	const blocksPerPiece = 4

	pieceLength := int64(blocksPerPiece) * meta.DefaultBlockSize
	totalLength := int64(numPieces) * pieceLength

	zeroPiece := make([]byte, pieceLength)
	hash := sha1.Sum(zeroPiece)
	pieces := make([]metainfo.Hash, numPieces)
	for i := range numPieces {
		pieces[i] = hash
	}

	info := meta.Info{
		Name:          "test",
		NumPieces:     numPieces,
		PieceLength:   pieceLength,
		LastPieceSize: pieceLength,
		TotalLength:   totalLength,
		Pieces:        pieces,
		Files:         []meta.File{{Path: "test", Length: totalLength}},
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()
	pp := NewPiecePicker(info, completedBm, wantedBm, nil, nil)

	// First complete piece 1 (so numCompletedPieces > 0, avoiding startup mode).
	for bi := range blocksPerPiece {
		pp.MarkAsRequesting(1, bi)
	}
	for bi := range blocksPerPiece {
		pp.MarkAsResponded(1, bi)
	}
	completedBm.Set(1) // simulate hash check passed
	pp.WeHave(1)       // increments numCompletedPieces to avoid startup mode

	// Then simulate downloading piece 0: all blocks requested and responded.
	for bi := range blocksPerPiece {
		pp.MarkAsRequesting(0, bi)
	}
	for bi := range blocksPerPiece {
		pp.MarkAsResponded(0, bi)
	}

	// At this point, piece 0 is allBlocksResponded → rebuildPriorities removes it.
	pp.rebuildPriorities(StrategyRarestFirst)

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

	pp.mu.Lock()
	t.Logf("After resetPiece: dirty=%v pieces=%v", pp.dirty, pp.pieces)
	t.Logf("allBlocksResponded(0)=%v", pp.allBlocksResponded(0))
	for bi := range blocksPerPiece {
		idx := pp.blockInfoIdx(0) + bi
		t.Logf("  block %d state: %d", bi, pp.blockInfos.get(idx))
	}
	pp.mu.Unlock()

	// After resetPiece, piece 0 should be pickable again.
	result.FreeBlocks = result.FreeBlocks[:0]

	pp.mu.Lock()
	t.Logf("Before pickPieces: dirty=%v pieces=%v", pp.dirty, pp.pieces)
	t.Logf("allBlocksResponded(0)=%v", pp.allBlocksResponded(0))
	pp.mu.Unlock()

	result = pp.PickPieces(bitfield, false, nil, 100, 0, nil, result)

	pp.mu.Lock()
	t.Logf("After pickPieces: pieces=%v", pp.pieces)
	pp.mu.Unlock()

	pp.mu.Lock()
	t.Logf("Before pickPieces: dirty=%v pieces=%v", pp.dirty, pp.pieces)
	t.Logf("allBlocksResponded(0)=%v", pp.allBlocksResponded(0))
	pp.mu.Unlock()

	result = pp.PickPieces(bitfield, false, nil, 100, 0, nil, result)

	pp.mu.Lock()
	t.Logf("After pickPieces: pieces=%v", pp.pieces)
	pp.mu.Unlock()

	found := false
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("piece 0 should be pickable after resetPiece (bug: not re-added to candidates)")
	}
}
