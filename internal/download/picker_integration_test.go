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

	peerBf := bm.NewLockFreeBitmap(numPieces)
	peerBf.Fill()
	for i := range numPieces {
		pp.IncRefcount(i)
	}

	last := make([]BlockClaim, 0, 16)
	var h heap.Heap[responseChunk]
	doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}

	// Feed chunks until picker returns nothing or we run out of iterations.
	const maxIters = 5000
	for iter := 0; iter < maxIters && d.completedBm.Count() < numPieces; iter++ {
		desired := 4 + int(blocksPerPiece*2)
		last = pp.PickAndClaim(last, PickRequest{
			Bitfield:      peerBf,
			BlockedPieces: bm.NewLockFreeBitmap(numPieces),
			PeerID:        1,
			NumBlocks:     desired,
		})

		if len(last) == 0 {
			time.Sleep(time.Millisecond)
			continue
		}

		n := 1 + rng.IntN(min(len(last), int(blocksPerPiece)))
		for i, claim := range last {
			if i >= n {
				pp.ReleaseClaim(claim)
				continue
			}
			fb := claim.Block
			chunkSize := defaultBlockSize
			if fb.PieceIndex == numPieces-1 {
				nb := d.info.PieceBlockCount(fb.PieceIndex)
				if fb.BlockIndex == uint32(nb-1) {
					chunkSize = int(d.info.PieceLen(fb.PieceIndex)) - (nb-1)*defaultBlockSize
				}
			}

			handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 1, claim: claim, res: &proto.ChunkResponse{
				PieceIndex: fb.PieceIndex,
				Begin:      fb.BlockIndex * uint32(defaultBlockSize),
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
