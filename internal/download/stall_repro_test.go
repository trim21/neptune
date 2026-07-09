// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// TestStallCompletedBmRace reproduces the "compl=2" stall: Phase 1 scans
// downloadingPieces without checking completedBm, returning blocks from
// already-completed pieces. requestABlock rejects them → stall.
func TestStallCompletedBmRace(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := NewPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Add piece 0 to downloadingPieces with some free blocks.
	pp.MarkAsRequesting(0, 0)
	pp.MarkAsRequesting(0, 1)
	pp.AddDownloadingPiece(0, info)

	// Simulate the race window after checkPiece succeeds:
	// completedBm.SetX runs but weHave hasn't removed from downloadingPieces yet.
	completedBm.SetX(0)

	// pickPieces → Phase 1 finds piece 0 in downloadingPieces, returns free blocks.
	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, 4, 0, nil, info, StrategyRarestFirst, result)

	badBlocks := 0
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			badBlocks++
		}
	}
	if badBlocks > 0 {
		t.Errorf("REGRESSION: Phase 1 returned %d free blocks from completed piece 0. "+
			"completedBm check should have filtered them.", badBlocks)
	}
}

// TestStallEndgameBusyLoop tests the full download pipeline near completion
// where the picker spins between compl=2 rejects and 0 free returns.
func TestStallEndgameBusyLoop(t *testing.T) {
	d := newTestDownload(t, 10, 4, piece_store.NewMemStore)

	// Complete pieces 2-9 (80% done).
	for pi := range uint32(10) {
		if pi <= 1 {
			continue
		}
		for bi := range d.info.PieceBlockCount(pi) {
			d.handleRes(&proto.ChunkResponse{
				PieceIndex: pi,
				Begin:      uint32(bi) * defaultBlockSize,
				Data:       make([]byte, defaultBlockSize),
			})
		}
	}
	time.Sleep(20 * time.Millisecond)

	// Only pieces 0 and 1 remain. Start both.
	d.picker.Load().MarkAsRequesting(0, 0)
	d.picker.Load().AddDownloadingPiece(0, d.info)
	d.picker.Load().MarkAsRequesting(1, 0)
	d.picker.Load().AddDownloadingPiece(1, d.info)

	// Receive all blocks for piece 1 → triggers async checkPiece.
	for bi := range d.info.PieceBlockCount(1) {
		if bi == 0 {
			continue
		}
		d.handleRes(&proto.ChunkResponse{
			PieceIndex: 1, Begin: uint32(bi) * defaultBlockSize,
			Data: make([]byte, defaultBlockSize),
		})
	}
	d.handleRes(&proto.ChunkResponse{
		PieceIndex: 1, Begin: 0,
		Data: make([]byte, defaultBlockSize),
	})

	time.Sleep(5 * time.Millisecond) // async checkPiece may be in-flight

	peerBitfield := bm.New(10)
	peerBitfield.Fill()

	pp := d.picker.Load()
	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, 100, 0, nil, d.info, StrategyRarestFirst, result)

	completedFromResult := 0
	for _, fb := range result.FreeBlocks {
		if d.completedBm.Contains(fb.PieceIndex) {
			completedFromResult++
		}
	}
	if completedFromResult > 0 {
		t.Logf("Race hit: %d free blocks from already-completed pieces", completedFromResult)
	}

	d.cancel()
	time.Sleep(10 * time.Millisecond)
}

func TestStall_NewPickerDoesntInheritDownloadingPieces(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	oldPicker := NewPiecePicker(info, completedBm, wantedBm)

	// Add piece 0 to OLD picker's downloadingPieces.
	for bi := range int(blocksPerPiece) {
		oldPicker.MarkAsRequesting(0, bi)
		oldPicker.MarkAsResponded(0, bi)
	}
	oldPicker.AddDownloadingPiece(0, info)

	// checkPiece completes: completedBm.SetX(0)
	completedBm.SetX(0)

	// Create NEW picker (strategy change race).
	newPicker := NewPiecePicker(info, completedBm, wantedBm)
	newPicker.MarkAsRequesting(1, 0)
	newPicker.MarkAsResponded(1, 0)
	completedBm.Set(1)
	newPicker.WeHave(1, info)

	// weHave(0) on NEW picker → no-op (piece 0 not in newPicker's downloadingPieces).
	newPicker.WeHave(0, info)

	// OLD picker still has piece 0 in downloadingPieces, all blocks responded.
	// If requestABlock loaded OLD picker before the swap → operates on stale state.
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	result := PickResult{}
	result = oldPicker.PickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as free block")
		}
	}
	for _, bb := range result.BusyBlocks {
		if bb.PieceIndex == 0 {
			t.Error("OLD picker leaked completed+zombie piece 0 as busy block")
		}
	}
}

// TestStallEndgamePicksFreeZeroPieces verifies that in endgame mode
// (numWantLeft==0), busy blocks are collected from downloadingPieces
// even when they have 0 free blocks (e.g. orphaned requested blocks).
func TestStallEndgamePicksFreeZeroPieces(t *testing.T) {
	const numPieces = uint32(3)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * defaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * defaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := NewPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Complete piece 0 to avoid startup mode.
	pp.MarkAsRequesting(0, 0)
	pp.MarkAsResponded(0, 0)
	completedBm.Set(0)
	pp.WeHave(0, info)

	// Piece 1: 0 free, 1 requested, 3 responded (stalled near completion).
	for bi := range int(blocksPerPiece) {
		pp.MarkAsRequesting(1, bi)
		pp.MarkAsResponded(1, bi)
	}
	// Re-mark block 0 as requesting (simulating orphan).
	pp.MarkAsRequesting(1, 0)

	// Piece 2: complete it so numWantLeft goes to 0 (triggers endgame).
	for bi := range int(blocksPerPiece) {
		pp.MarkAsRequesting(2, bi)
		pp.MarkAsResponded(2, bi)
	}
	completedBm.Set(2)
	pp.WeHave(2, info)

	// Now: numWantLeft == 0 (all pieces either completed or in downloadingPieces).
	// Piece 1 has 0 free, 1 requested, 3 responded.
	// Endgame fallback should collect piece 1's requested block as busy.

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	// Should have collected the busy block from piece 1.
	hasBusy := false
	for _, bb := range result.BusyBlocks {
		if bb.PieceIndex == 1 {
			hasBusy = true
		}
	}
	if !hasBusy {
		t.Error("Endgame fallback did not collect busy block from free=0 piece 1")
	} else {
		t.Log("Endgame fallback correctly collected busy block from stalled piece 1")
	}
}

var _ = context.Background
