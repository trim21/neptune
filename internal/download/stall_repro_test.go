// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/heap"
	"neptune/internal/proto"
)

// TestStallEndgameBusyLoop tests the full download pipeline near completion
// where the picker spins between compl=2 rejects and 0 free returns.
func TestStallEndgameBusyLoop(t *testing.T) {
	d := newTestDownload(t, 10, 4, piece_store.NewMemStore)

	var h heap.Heap[responseChunk]
	doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}

	// Complete pieces 2-9 (80% done).
	for pi := range uint32(10) {
		if pi <= 1 {
			continue
		}
		for bi := range d.info.PieceBlockCount(pi) {
			handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
				PieceIndex: pi,
				Begin:      uint32(bi) * defaultBlockSize,
				Data:       make([]byte, defaultBlockSize),
			}})
		}
	}
	time.Sleep(20 * time.Millisecond)

	// Only pieces 0 and 1 remain. Start both.
	d.picker.Load().MarkAsRequesting(0, 0)
	d.picker.Load().AddDownloadingPiece(0)
	d.picker.Load().MarkAsRequesting(1, 0)
	d.picker.Load().AddDownloadingPiece(1)

	// Receive all blocks for piece 1 → triggers async checkPiece.
	for bi := range d.info.PieceBlockCount(1) {
		if bi == 0 {
			continue
		}
		handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
			PieceIndex: 1, Begin: uint32(bi) * defaultBlockSize,
			Data: make([]byte, defaultBlockSize),
		}})
	}
	handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
		PieceIndex: 1, Begin: 0,
		Data: make([]byte, defaultBlockSize),
	}})

	time.Sleep(5 * time.Millisecond) // async checkPiece may be in-flight

	peerBitfield := bm.New(10)
	peerBitfield.Fill()

	pp := d.picker.Load()
	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, bm.NewLockFreeBitmap(d.info.NumPieces), 100, 0, nil, result)

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

var _ = context.Background
