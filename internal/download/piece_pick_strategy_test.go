// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"

	"go.uber.org/atomic"

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

	strategy := new(atomic.Uint32)
	strategy.Store(uint32(StrategySequential))
	pp := NewPiecePicker(info, completedBm, wantedBm, nil, strategy)

	peerBitfield := bm.New(10)
	peerBitfield.Fill() // peer has everything

	var result PickResult

	// Sequential: should always pick block 0 of piece 0 first.
	result = pp.PickPieces(peerBitfield, false, nil, 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.FreeBlocks[0].PieceIndex)
	}
	if result.FreeBlocks[0].BlockIndex != 0 {
		t.Fatalf("expected block 0, got %d", result.FreeBlocks[0].BlockIndex)
	}

	// Mark block 0 of piece 0 as requesting.
	pp.MarkAsRequesting(0, 0)
	pp.AddDownloadingPiece(0)

	// Next request: should be block 1 of piece 0 (still sequential, finishing piece 0).
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 0 {
		t.Fatalf("expected piece 0, got %d", result.FreeBlocks[0].PieceIndex)
	}
	if result.FreeBlocks[0].BlockIndex != 1 {
		t.Fatalf("expected block 1 of piece 0, got block %d", result.FreeBlocks[0].BlockIndex)
	}

	// Complete piece 0 — mark all blocks as responded.
	for i := range 10 {
		pp.MarkAsResponded(0, i)
	}
	pp.WeHave(0)

	// Next request: piece 1, block 0 (sequential, piece 0 is done).
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, 1, 0, nil, result)
	if len(result.FreeBlocks) != 1 {
		t.Fatalf("expected 1 free block, got %d", len(result.FreeBlocks))
	}
	if result.FreeBlocks[0].PieceIndex != 1 {
		t.Fatalf("expected piece 1, got %d", result.FreeBlocks[0].PieceIndex)
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
