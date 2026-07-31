// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/bm"
)

// TestPickStartupBlockSkipsBlockedPieces verifies that startup mode
// (zero completed pieces, zero downloading pieces) never picks a block
// from a piece in the peer's blockedPieces set. Without this check the
// peer would re-request a piece whose hash check already failed.
func TestPickStartupBlockSkipsBlockedPieces(t *testing.T) {
	pp := newTestPicker(4, 4)

	blocked := bm.NewLockFreeBitmap(pp.info.NumPieces)
	blocked.Set(1)

	bitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	bitfield.Fill()

	// Repeated picks cover the random middle-50% candidate selection.
	for range 100 {
		result := pp.PickPieces(bitfield, false, nil, blocked, 4, 0, nil, false, 123, PickResult{})
		require.NotEmpty(t, result.FreeBlocks, "startup mode should pick a block from an unblocked piece")
		for _, blk := range result.FreeBlocks {
			require.False(t, blocked.Contains(blk.PieceIndex), "startup mode must not pick blocked piece %d", blk.PieceIndex)
		}
	}
}

// TestPickStartupBlockAllBlockedReturnsEmpty verifies that when every
// available piece is blocked, startup mode falls through without picking
// anything instead of selecting a blocked piece.
func TestPickStartupBlockAllBlockedReturnsEmpty(t *testing.T) {
	pp := newTestPicker(4, 4)

	blocked := bm.NewLockFreeBitmap(pp.info.NumPieces)
	blocked.Fill()

	bitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	bitfield.Fill()

	result := pp.PickPieces(bitfield, false, nil, blocked, 4, 0, nil, false, 123, PickResult{})
	require.Empty(t, result.FreeBlocks)
}
