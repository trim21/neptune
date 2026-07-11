// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"math/rand/v2"
	"testing"

	"neptune/internal/pkg/bm"
)

// FuzzPiecePicker exercises the picker with random peer arrivals and block
// completions, verifying that all blocks are eventually picked and completed.
func FuzzPiecePicker(f *testing.F) {
	f.Add(uint32(5), uint32(4), uint64(42))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed uint64) {
		numPieces = 2 + numPieces%8           // 2-9 pieces
		blocksPerPiece = 2 + blocksPerPiece%7 // 2-8 blocks per piece
		totalBlocks := int(numPieces * blocksPerPiece)

		pp := newTestPicker(numPieces, blocksPerPiece)
		rng := rand.New(rand.NewPCG(seed, seed>>32))

		// Random initial state: complete 0-30% of pieces upfront.
		initComplete := rng.IntN(int(numPieces/3 + 1))
		initPieces := randomPieces(rng, int(numPieces), initComplete)
		for _, pi := range initPieces {
			setPieceResponded(pp, pi, int(blocksPerPiece))
			pp.missingBm.Unset(pi)
			pp.WeHave(pi)
		}

		completed := bm.New(numPieces)
		for _, pi := range initPieces {
			completed.Set(pi)
		}

		// Track per-piece block progress.
		pieceDone := make([]int, numPieces)
		for _, pi := range initPieces {
			pieceDone[pi] = int(blocksPerPiece)
		}

		last := PickResult{
			FreeBlocks: make([]PieceBlock, 0, 16),
			BusyBlocks: make([]PieceBlock, 0, 16),
		}

		// Use a fixed peer that has all pieces, refcount set once.
		fullPeer := bm.New(numPieces)
		fullPeer.Fill()
		for i := range numPieces {
			pp.IncRefcount(i)
		}

		const maxIters = 5000
		for range maxIters {
			if int(completed.Count()) == int(numPieces) {
				break
			}

			choked := rng.IntN(2) == 0
			fastBm := randomFastBitmap(rng, numPieces, fullPeer)
			desired := min(4+int(blocksPerPiece), 16)
			maxNumReq := 4 + int(blocksPerPiece)
			outstanding := rng.IntN(maxNumReq)
			queued := rng.IntN(maxNumReq)

			last = pp.RequestABlock(last, desired, outstanding, queued, choked, fullPeer, fastBm, bm.NewLockFreeBitmap(numPieces))

			if len(last.FreeBlocks) == 0 && len(last.BusyBlocks) == 0 {
				continue
			}

			// Simulate receiving blocks: randomly complete 1-N blocks.
			pickFrom := last.FreeBlocks
			if len(pickFrom) == 0 {
				pickFrom = last.BusyBlocks
			}
			n := 1 + rng.IntN(min(len(pickFrom), 3))
			for i := 0; i < n && i < len(pickFrom); i++ {
				fb := pickFrom[i]
				if pieceDone[fb.PieceIndex] == 0 {
					pp.AddDownloadingPiece(fb.PieceIndex)
				}
				pp.MarkAsRequesting(fb.PieceIndex, fb.BlockIndex)
				pp.MarkAsResponded(fb.PieceIndex, fb.BlockIndex)
				pieceDone[fb.PieceIndex]++

				if pieceDone[fb.PieceIndex] == int(blocksPerPiece) {
					pp.missingBm.Unset(fb.PieceIndex)
					pp.WeHave(fb.PieceIndex)
					completed.Set(fb.PieceIndex)
				}
			}
		}

		if int(completed.Count()) < int(numPieces) {
			t.Errorf("seed=%d: stalled at %d/%d pieces after %d iters",
				seed, int(completed.Count()), numPieces, maxIters)
		} else if int(completed.Count()) == int(numPieces) && int(blocksPerPiece)*int(numPieces) > 0 {
			// All done — verify total blocks accounted for.
			totalDone := 0
			for _, d := range pieceDone {
				totalDone += d
			}
			if totalDone != totalBlocks {
				t.Errorf("seed=%d: block count mismatch: done=%d total=%d",
					seed, totalDone, totalBlocks)
			}
		}
	})
}

func randomPieces(rng *rand.Rand, total, count int) []uint32 {
	if count == 0 {
		return nil
	}
	indices := make([]uint32, total)
	for i := range total {
		indices[i] = uint32(i)
	}
	rng.Shuffle(total, func(i, j int) {
		indices[i], indices[j] = indices[j], indices[i]
	})
	return indices[:count]
}

func randomFastBitmap(rng *rand.Rand, numPieces uint32, peerBf *bm.Bitmap) *bm.Bitmap {
	if rng.IntN(2) == 0 {
		return nil
	}
	fast := bm.New(numPieces)
	var pieces []uint32
	peerBf.Range(func(pi uint32) {
		pieces = append(pieces, pi)
	})
	if len(pieces) == 0 {
		return nil
	}
	n := 1 + rng.IntN(len(pieces))
	rng.Shuffle(len(pieces), func(i, j int) {
		pieces[i], pieces[j] = pieces[j], pieces[i]
	})
	for _, pi := range pieces[:n] {
		fast.Set(pi)
	}
	return fast
}

func setPieceResponded(pp *PiecePicker, pieceIndex uint32, blocksPerPiece int) {
	for bi := range blocksPerPiece {
		pp.MarkAsRequesting(pieceIndex, bi)
		pp.MarkAsResponded(pieceIndex, bi)
	}
}
