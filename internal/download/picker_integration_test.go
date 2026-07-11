// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"math/rand/v2"
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/heap"
	"neptune/internal/proto"
)

// FuzzPickerDownloadIntegration tests the full loop:
// picker → peer enqueue → handleRes → piece complete → loop.
func FuzzPickerDownloadIntegration(f *testing.F) {
	f.Add(uint32(5), uint32(4), uint64(42))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed uint64) {
		numPieces = 2 + numPieces%7
		blocksPerPiece = 2 + blocksPerPiece%7
		runIntegration(t, numPieces, blocksPerPiece, seed)
	})
}

func TestPickerDownloadIntegration(t *testing.T) {
	for _, seed := range []uint64{42, 12345, 99999} {
		for numPieces := uint32(2); numPieces <= 6; numPieces++ {
			for blocksPerPiece := uint32(2); blocksPerPiece <= 6; blocksPerPiece++ {
				runIntegration(t, numPieces, blocksPerPiece, seed)
			}
		}
	}
}

func runIntegration(t *testing.T, numPieces, blocksPerPiece uint32, seed uint64) {
	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
	d.state.Store(uint32(Downloading))
	defer d.cancel()
	pp := d.picker.Load()

	rng := rand.New(rand.NewPCG(seed, seed>>32))

	peerBf := bm.New(numPieces)
	peerBf.Fill()
	for i := range numPieces {
		pp.IncRefcount(i)
	}

	last := PickResult{
		FreeBlocks: make([]PieceBlock, 0, 16),
		BusyBlocks: make([]PieceBlock, 0, 16),
	}
	pieceDone := make([]int, numPieces)

	var h heap.Heap[responseChunk]
	doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}

	// Feed chunks until picker returns nothing or we run out of iterations.
	const maxIters = 5000
	for iter := 0; iter < maxIters && d.completedBm.Count() < numPieces; iter++ {
		desired := 4 + int(blocksPerPiece*2)
		last = pp.RequestABlock(last, desired, 0, 0, false, peerBf, nil, bm.NewLockFreeBitmap(numPieces))

		if len(last.FreeBlocks) == 0 && len(last.BusyBlocks) == 0 {
			time.Sleep(time.Millisecond)
			continue
		}

		pickFrom := last.FreeBlocks
		if len(pickFrom) == 0 {
			pickFrom = last.BusyBlocks
		}
		n := 1 + rng.IntN(min(len(pickFrom), int(blocksPerPiece)))
		for i := 0; i < n && i < len(pickFrom); i++ {
			fb := pickFrom[i]
			pp.MarkAsRequesting(fb.PieceIndex, fb.BlockIndex)
			if pieceDone[fb.PieceIndex] == 0 {
				pp.AddDownloadingPiece(fb.PieceIndex)
			}
			pieceDone[fb.PieceIndex]++

			chunkSize := defaultBlockSize
			if fb.PieceIndex == numPieces-1 {
				nb := d.info.PieceBlockCount(fb.PieceIndex)
				if fb.BlockIndex == nb-1 {
					chunkSize = int(d.info.PieceLen(fb.PieceIndex)) - (nb-1)*defaultBlockSize
				}
			}

			handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
				PieceIndex: fb.PieceIndex,
				Begin:      uint32(fb.BlockIndex) * uint32(defaultBlockSize),
				Data:       make([]byte, chunkSize),
			}})
		}
	}

	// Wait for async checkPiece goroutines.
	ok := false
	for range 50 {
		time.Sleep(50 * time.Millisecond)
		if d.completedBm.Count() == numPieces {
			ok = true
			break
		}
	}
	if !ok {
		t.Fatalf("seed=%d: stalled at %d/%d pieces",
			seed, d.completedBm.Count(), numPieces)
	}
}
