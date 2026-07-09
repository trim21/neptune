// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

// seededRand is a minimal xorshift64* RNG, sufficient for deterministic shuffles.
type seededRand struct {
	seed uint64
}

func (r *seededRand) next() uint64 {
	r.seed ^= r.seed >> 12
	r.seed ^= r.seed << 25
	r.seed ^= r.seed >> 27
	return r.seed * 0x2545F4914F6CDD1D
}

// TestStallZombiePiece reproduces the "0 free, 0 busy" pattern:
// A piece has all blocks responded (hash check pending), rebuildPriorities
// removes it from pp.pieces, and if it also disappears from downloadingPieces
// (e.g. via picker swap), it becomes unreachable.
func TestStallZombiePiece(t *testing.T) {
	const numPieces = uint32(10)
	const blocksPerPiece = uint32(4)

	info := meta.Info{
		NumPieces:     numPieces,
		PieceLength:   int64(blocksPerPiece) * meta.DefaultBlockSize,
		LastPieceSize: int64(blocksPerPiece) * meta.DefaultBlockSize,
	}

	completedBm := bm.New(numPieces)
	wantedBm := bm.New(numPieces)
	wantedBm.Fill()

	pp := NewPiecePicker(info, completedBm, wantedBm)
	peerBitfield := bm.New(numPieces)
	peerBitfield.Fill()

	// Complete piece 1 to avoid startup mode.
	pp.MarkAsRequesting(1, 0)
	pp.MarkAsResponded(1, 0)
	completedBm.Set(1)
	pp.WeHave(1, info)

	// Simulate: piece 0 fully received, all blocks responded, hash check pending.
	for bi := range int(blocksPerPiece) {
		pp.MarkAsRequesting(0, bi)
		pp.MarkAsResponded(0, bi)
	}
	pp.AddDownloadingPiece(0, info)

	// Manually remove from downloadingPieces (simulating picker swap race).
	pp.mu.Lock()
	for i, dp := range pp.downloadingPieces {
		if dp.index == 0 {
			pp.downloadingPieces = append(pp.downloadingPieces[:i], pp.downloadingPieces[i+1:]...)
			break
		}
	}
	pp.mu.Unlock()

	// Now: piece 0 NOT in downloadingPieces, NOT in completedBm,
	//      all blocks are blockStateResponded.
	// rebuildPriorities filters it out (allBlocksResponded) → pp.pieces sans 0.

	result := PickResult{}
	result = pp.PickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			t.Error("BUG NOT REPRODUCED? Piece 0 still pickable via free blocks")
		}
	}
	for _, bb := range result.BusyBlocks {
		if bb.PieceIndex == 0 {
			t.Error("BUG NOT REPRODUCED? Piece 0 still pickable via busy blocks")
		}
	}

	t.Logf("BUG REPRODUCED: zombie piece 0 invisible to picker, total=%d free + %d busy",
		len(result.FreeBlocks), len(result.BusyBlocks))

	// Recovery: resetPiece should bring piece 0 back.
	pp.ResetPiece(0, info)
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]
	result = pp.PickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

	recovered := false
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == 0 {
			recovered = true
		}
	}
	if !recovered {
		t.Error("resetPiece failed to recover zombie piece 0")
	}
}

// FuzzStallNearCompletion fuzzes the picker state space to find scenarios
// where PickPieces returns 0 valid blocks despite remaining pieces.
func FuzzStallNearCompletion(f *testing.F) {
	f.Add(uint32(5), uint32(4), int64(12345))
	f.Add(uint32(10), uint32(4), int64(42))
	f.Add(uint32(8), uint32(6), int64(9999))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed int64) {
		numPieces = max(2, min(numPieces, 20))
		blocksPerPiece = max(2, min(blocksPerPiece, 10))

		info := meta.Info{
			NumPieces:     numPieces,
			PieceLength:   int64(blocksPerPiece) * meta.DefaultBlockSize,
			LastPieceSize: int64(blocksPerPiece) * meta.DefaultBlockSize,
		}

		completedBm := bm.New(numPieces)
		wantedBm := bm.New(numPieces)
		wantedBm.Fill()

		pp := NewPiecePicker(info, completedBm, wantedBm)
		peerBitfield := bm.New(numPieces)
		peerBitfield.Fill()

		rng := &seededRand{seed: uint64(seed)}

		// Complete piece 0 to avoid startup mode.
		pp.MarkAsRequesting(0, 0)
		pp.MarkAsResponded(0, 0)
		completedBm.Set(0)
		pp.WeHave(0, info)

		// Randomly assign states to remaining pieces.
		for pi := uint32(1); pi < numPieces; pi++ {
			state := rng.next() % 5
			switch state {
			case 0: // pristine
			case 1: // partially requested
				nReq := int(rng.next()%uint64(blocksPerPiece)) + 1
				for bi := range nReq {
					pp.MarkAsRequesting(pi, bi)
				}
				pp.AddDownloadingPiece(pi, info)
			case 2: // fully responded, in downloadingPieces
				for bi := range int(blocksPerPiece) {
					pp.MarkAsRequesting(pi, bi)
					pp.MarkAsResponded(pi, bi)
				}
				pp.AddDownloadingPiece(pi, info)
			case 3: // zombie: fully responded, NOT in downloadingPieces
				for bi := range int(blocksPerPiece) {
					pp.MarkAsRequesting(pi, bi)
					pp.MarkAsResponded(pi, bi)
				}
				pp.AddDownloadingPiece(pi, info)
				pp.mu.Lock()
				for j, dp := range pp.downloadingPieces {
					if dp.index == pi {
						pp.downloadingPieces = append(pp.downloadingPieces[:j], pp.downloadingPieces[j+1:]...)
						break
					}
				}
				pp.mu.Unlock()
			case 4: // race: completedBm set, still in downloadingPieces
				pp.MarkAsRequesting(pi, 0)
				pp.AddDownloadingPiece(pi, info)
				completedBm.SetX(pi)
			}
		}

		var result PickResult
		result = pp.PickPieces(peerBitfield, false, nil, 100, 0, nil, info, StrategyRarestFirst, result)

		// Invariant: Phase 1 now checks completedBm, so no completed-piece blocks
		// should leak into the result.
		badBlocks := 0
		for _, fb := range result.FreeBlocks {
			if pp.completedBm.Contains(fb.PieceIndex) {
				badBlocks++
			}
		}
		for _, bb := range result.BusyBlocks {
			if pp.completedBm.Contains(bb.PieceIndex) {
				badBlocks++
			}
		}
		if badBlocks > 0 {
			t.Errorf("seed=%d: %d blocks from completed pieces leaked through Phase 1", seed, badBlocks)
		}

		// Invariant: No block from zombie pieces (responded, not in downloadingPieces,
		// not in completedBm — should be excluded by rebuildPriorities).
		zombieBlocks := 0
		pp.mu.Lock()
		for _, fb := range result.FreeBlocks {
			if pp.allBlocksResponded(fb.PieceIndex, info) && !pp.completedBm.Contains(fb.PieceIndex) {
				if pp.findDownloadingPiece(fb.PieceIndex) == nil {
					zombieBlocks++
				}
			}
		}
		pp.mu.Unlock()
		if zombieBlocks > 0 {
			t.Errorf("seed=%d: %d zombie blocks leaked (should be filtered)", seed, zombieBlocks)
		}
	})
}
