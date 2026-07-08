// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

func TestPiecePickStrategy_Sequential(t *testing.T) {
	// Setup: small torrent with 10 pieces, 10 blocks/piece.
	info := meta.Info{
		NumPieces:     10,
		PieceLength:   10 * defaultBlockSize,
		LastPieceSize: 10 * defaultBlockSize,
	}

	completedBm := bm.New(10)
	wantedBm := bm.New(10)
	wantedBm.Fill()

	pp := newPiecePicker(info, completedBm, wantedBm)

	peerBitfield := bm.New(10)
	peerBitfield.Fill() // peer has everything

	var result pickResult

	// Sequential: should always pick block 0 of piece 0 first.
	result = pp.pickPieces(peerBitfield, false, nil, 1, 0, nil, info, StrategySequential, result)
	if len(result.freeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.freeBlocks))
	}
	if result.freeBlocks[0].pieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.freeBlocks[0].pieceIndex)
	}
	if result.freeBlocks[0].blockIndex != 0 {
		t.Fatalf("expected block 0, got %d", result.freeBlocks[0].blockIndex)
	}

	// Mark block 0 of piece 0 as requesting.
	pp.markAsRequesting(0, 0)
	pp.addDownloadingPiece(0, info)

	// Next request: should be block 1 of piece 0 (still sequential, finishing piece 0).
	result.freeBlocks = result.freeBlocks[:0]
	result.busyBlocks = result.busyBlocks[:0]
	result = pp.pickPieces(peerBitfield, false, nil, 1, 0, nil, info, StrategySequential, result)
	if len(result.freeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.freeBlocks))
	}
	if result.freeBlocks[0].pieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.freeBlocks[0].pieceIndex)
	}
	if result.freeBlocks[0].blockIndex != 1 {
		t.Fatalf("expected block 1 of piece 0, got block %d", result.freeBlocks[0].blockIndex)
	}

	// Complete piece 0 — mark all blocks as responded.
	for i := range 10 {
		pp.markAsResponded(0, i)
	}
	pp.weHave(0, info)

	// Next request: piece 1, block 0 (sequential, piece 0 is done).
	result.freeBlocks = result.freeBlocks[:0]
	result.busyBlocks = result.busyBlocks[:0]
	result = pp.pickPieces(peerBitfield, false, nil, 1, 0, nil, info, StrategySequential, result)
	if len(result.freeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.freeBlocks))
	}
	if result.freeBlocks[0].pieceIndex != 1 {
		t.Fatalf("expected piece 1, got %d", result.freeBlocks[0].pieceIndex)
	}
}

func TestPiecePickStrategy_DefaultIsRarestFirst(t *testing.T) {
	if defaultStrategy("") != StrategyRarestFirst {
		t.Fatal("empty config should default to rarest-first")
	}
	if defaultStrategy("unknown") != StrategyRarestFirst {
		t.Fatal("unknown value should default to rarest-first")
	}
	if defaultStrategy("sequential") != StrategySequential {
		t.Fatal("sequential should be recognized")
	}
	if defaultStrategy("rarest-first") != StrategyRarestFirst {
		t.Fatal("rarest-first should be recognized")
	}
}

func TestPiecePickStrategy_String(t *testing.T) {
	if StrategyRarestFirst.String() != "rarest-first" {
		t.Fatal("rarest-first string")
	}
	if StrategySequential.String() != "sequential" {
		t.Fatal("sequential string")
	}
	// Unknown values return "<invalid>"
	if PiecePickStrategy(255).String() != "<invalid>" {
		t.Fatal("unknown should be <invalid>")
	}
}

func TestPiecePickStrategy_FromString(t *testing.T) {
	for _, s := range []string{"rarest-first", "sequential"} {
		v, err := PiecePickStrategyFromString(s)
		if err != nil {
			t.Fatalf("failed to parse %q: %v", s, err)
		}
		if v.String() != s {
			t.Fatalf("roundtrip failed: %s -> %s", s, v.String())
		}
	}

	_, err := PiecePickStrategyFromString("unknown")
	if err == nil {
		t.Fatal("should fail for unknown value")
	}
}
