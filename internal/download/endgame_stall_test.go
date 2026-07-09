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
		pp.addDownloadingPiece(pi, info)
		for bi := range int(info.PieceBlockCount(pi)) {
			pp.markAsRequesting(pi, bi)
			pp.markAsResponded(pi, bi)
		}
		pp.weHave(pi, info)
	}

	// Piece 3: all blocks responded (pending hash check).
	pp.addDownloadingPiece(3, info)
	for bi := range 4 {
		pp.markAsRequesting(3, bi)
		pp.markAsResponded(3, bi)
	}

	// Piece 4: 2 requested, 2 responded, 0 free.
	pp.addDownloadingPiece(4, info)
	pp.markAsRequesting(4, 0)
	pp.markAsRequesting(4, 1)
	pp.markAsResponded(4, 0)
	pp.markAsResponded(4, 1)
	pp.markAsRequesting(4, 2)
	pp.markAsRequesting(4, 3)

	result := pp.pickPieces(bitfield, false, nil, 4, 0, nil, info, StrategyRarestFirst, pickResult{})

	if len(result.freeBlocks) == 0 && len(result.busyBlocks) == 0 {
		t.Error("BUG: pickPieces returned empty when busy blocks exist (99% stall)")
	}

	if len(result.busyBlocks) > 0 {
		for _, bb := range result.busyBlocks {
			if bb.pieceIndex < 4 {
				t.Error("busy block from non-target piece")
			}
		}
	}
}

// pickerEnv creates a fresh piecePicker for testing.
func pickerEnv(numPieces uint32, blocksPerPiece uint32) (*piecePicker, meta.Info) {
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

	return newPiecePicker(info, completedBm, wantedBm), info
}
