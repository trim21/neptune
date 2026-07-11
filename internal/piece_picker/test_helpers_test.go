// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

// testInfo builds a minimal meta.Info for testing with the given number of
// pieces and blocks per piece.
func testInfo(numPieces, blocksPerPiece uint32) meta.Info {
	pieceLen := int64(blocksPerPiece) * meta.DefaultBlockSize
	return meta.Info{
		NumPieces:     numPieces,
		PieceLength:   pieceLen,
		LastPieceSize: pieceLen,
	}
}

// newTestPicker creates a fully initialized PiecePicker for testing.
// All pieces are wanted, none completed.
func newTestPicker(numPieces, blocksPerPiece uint32) *PiecePicker {
	info := testInfo(numPieces, blocksPerPiece)
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	missingBm.Fill()
	chunkDoneBm := bm.New(info.NumPieces * blocksPerPiece)

	return NewPiecePicker(info, missingBm, chunkDoneBm, new(atomic.Uint32))
}
