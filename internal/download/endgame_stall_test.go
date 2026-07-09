// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"crypto/sha1"
	"testing"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
)

// TestEndgameNoFreeBlocks reproduces the 99% stall where all remaining
// blocks are either requested (from stale/disconnected peers) or responded
// (pending hash check), with zero free blocks. The endgame should return
// busy blocks so the peer can re-request them.
func TestEndgameNoFreeBlocks(t *testing.T) {
	pp, info := pickerEnv(5, 4)
	bitfield := bm.New(info.NumPieces)
	bitfield.Fill()

	// Complete pieces 0-2.
	for pi := range uint32(3) {
		pp.AddDownloadingPiece(pi, info)
		for bi := range info.PieceBlockCount(pi) {
			pp.MarkAsRequesting(pi, bi)
			pp.MarkAsResponded(pi, bi)
		}
		pp.WeHave(pi, info)
	}

	// Piece 3: all blocks responded (pending hash check).
	pp.AddDownloadingPiece(3, info)
	for bi := range 4 {
		pp.MarkAsRequesting(3, bi)
		pp.MarkAsResponded(3, bi)
	}

	// Piece 4: 2 requested, 2 responded, 0 free.
	pp.AddDownloadingPiece(4, info)
	pp.MarkAsRequesting(4, 0)
	pp.MarkAsRequesting(4, 1)
	pp.MarkAsResponded(4, 0)
	pp.MarkAsResponded(4, 1)
	pp.MarkAsRequesting(4, 2)
	pp.MarkAsRequesting(4, 3)

	result := pp.PickPieces(bitfield, false, nil, 4, 0, nil, info, StrategyRarestFirst, PickResult{})

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

// pickerEnv creates a fresh PiecePicker for testing.
func pickerEnv(numPieces uint32, blocksPerPiece uint32) (*PiecePicker, meta.Info) {
	pieceLength := int64(blocksPerPiece) * defaultBlockSize
	totalLength := int64(numPieces) * pieceLength

	hashes := make([]metainfo.Hash, numPieces)
	for i := range numPieces {
		hashes[i] = sha1.Sum(make([]byte, pieceLength))
	}

	info := meta.Info{
		Name:          "test",
		NumPieces:     numPieces,
		PieceLength:   pieceLength,
		LastPieceSize: pieceLength,
		TotalLength:   totalLength,
		Pieces:        hashes,
		Files:         []meta.File{{Path: "test", Length: totalLength}},
	}

	completedBm := bm.New(info.NumPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()

	return NewPiecePicker(info, completedBm, wantedBm, nil, nil, false), info
}
