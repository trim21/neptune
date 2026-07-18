// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/bm"
)

func pickRequestForTest(pp *PiecePicker, peerID uint64, numBlocks int) PickRequest {
	bitfield := bm.NewLockFreeBitmap(pp.info.NumPieces)
	bitfield.Fill()
	return PickRequest{
		Bitfield:      bitfield,
		BlockedPieces: bm.NewLockFreeBitmap(pp.info.NumPieces),
		PeerID:        peerID,
		NumBlocks:     numBlocks,
	}
}

func TestPickAndClaimConcurrentFreeBlocksAreUnique(t *testing.T) {
	const blocks = 16
	pp := newTestPicker(1, blocks)
	pp.AddDownloadingPiece(0)

	claims := make(chan BlockClaim, blocks)
	var wg sync.WaitGroup
	for peerID := uint64(1); peerID <= blocks; peerID++ {
		wg.Go(func() {
			picked := pp.PickAndClaim(nil, pickRequestForTest(pp, peerID, 1))
			if len(picked) == 1 {
				claims <- picked[0]
			}
		})
	}
	wg.Wait()
	close(claims)

	seen := make(map[PieceBlock]struct{}, blocks)
	for claim := range claims {
		require.NotContains(t, seen, claim.Block)
		seen[claim.Block] = struct{}{}
	}
	require.Len(t, seen, blocks)
	require.Equal(t, blocks, pp.DebugStats().ActiveClaims)
}

func TestNamedEndgameClaimsReleaseIndependently(t *testing.T) {
	pp := newTestPicker(1, 1)
	first := pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1))
	second := pp.PickAndClaim(nil, pickRequestForTest(pp, 2, 1))
	third := pp.PickAndClaim(nil, pickRequestForTest(pp, 3, 1))
	fourth := pp.PickAndClaim(nil, pickRequestForTest(pp, 4, 1))
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Len(t, third, 1)
	require.Empty(t, fourth)
	require.Equal(t, 2, pp.DebugStats().DuplicateClaims)

	require.True(t, pp.ReleaseClaim(second[0]))
	require.True(t, pp.ReleaseClaim(first[0]))
	require.Equal(t, 1, pp.DebugStats().ActiveClaims)
	require.Equal(t, 1, pp.DebugStats().RequestedBlocks)

	require.True(t, pp.ReleaseClaim(third[0]))
	stats := pp.DebugStats()
	require.Zero(t, stats.ActiveClaims)
	require.Zero(t, stats.RequestedBlocks)
	require.Equal(t, 1, stats.FreeBlocks)
}

func TestAcceptResponseInvalidatesEndgameSiblings(t *testing.T) {
	pp := newTestPicker(1, 1)
	first := pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1))[0]
	second := pp.PickAndClaim(nil, pickRequestForTest(pp, 2, 1))[0]

	require.True(t, pp.AcceptResponse(second))
	require.False(t, pp.AcceptResponse(first))
	require.False(t, pp.ReleaseClaim(first))
	stats := pp.DebugStats()
	require.Zero(t, stats.ActiveClaims)
	require.Zero(t, stats.RequestedBlocks)
	require.Equal(t, 1, stats.RespondedBlocks)
	require.Equal(t, uint64(1), stats.StaleAccepts)
	require.Equal(t, uint64(1), stats.StaleReleases)
}

func TestStaleClaimCannotReleaseReclaimedBlock(t *testing.T) {
	pp := newTestPicker(1, 1)
	oldClaim := pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1))[0]
	require.True(t, pp.ReleaseClaim(oldClaim))

	newClaim := pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1))[0]
	require.NotEqual(t, oldClaim.token, newClaim.token)
	require.False(t, pp.ReleaseClaim(oldClaim))
	require.Equal(t, 1, pp.DebugStats().ActiveClaims)
	require.True(t, pp.AcceptResponse(newClaim))
}

func TestRequestGateControlsPickAndClaim(t *testing.T) {
	pp, state := newTestPickerWithState(1, 1)
	pp.AddDownloadingPiece(0)

	state.Store(2)
	require.Empty(t, pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1)))

	state.Store(1)
	claim := pp.PickAndClaim(nil, pickRequestForTest(pp, 1, 1))
	require.Len(t, claim, 1)
	require.True(t, pp.ReleaseClaim(claim[0]))
}

func TestRequestGateRaceEventuallyReleasesClaims(t *testing.T) {
	pp, state := newTestPickerWithState(4, 4)
	for pieceIndex := range uint32(4) {
		pp.AddDownloadingPiece(pieceIndex)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for peerID := uint64(1); peerID <= 4; peerID++ {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				claims := pp.PickAndClaim(nil, pickRequestForTest(pp, peerID, 4))
				for _, claim := range claims {
					pp.ReleaseClaim(claim)
				}
			}
		})
	}

	for range 1_000 {
		state.Store(2)
		runtime.Gosched()
		state.Store(1)
	}
	close(stop)
	wg.Wait()
	pp.ReleaseAllClaims()

	stats := pp.DebugStats()
	require.Zero(t, stats.ActiveClaims)
	require.Zero(t, stats.RequestedBlocks)
}

func TestReleaseAllAndResetInvalidateClaimsWithoutReusingTokens(t *testing.T) {
	pp := newTestPicker(2, 4)
	pp.AddDownloadingPiece(0)
	claims := pp.PickAndClaim(nil, pickRequestForTest(pp, 7, 4))
	require.Len(t, claims, 4)
	lastToken := claims[len(claims)-1].token
	require.Equal(t, 4, pp.ReleaseAllClaims())
	require.Zero(t, pp.DebugStats().ActiveClaims)

	pp.ResetAll()
	pp.AddDownloadingPiece(0)
	claim := pp.PickAndClaim(nil, pickRequestForTest(pp, 7, 1))[0]
	require.Greater(t, claim.token, lastToken)
	require.False(t, pp.ReleaseClaim(claims[0]))
}

func TestEmptyPickDiagnosticsAreRateLimited(t *testing.T) {
	pp := newTestPicker(4, 1)
	empty := bm.NewLockFreeBitmap(pp.numPieces)
	blocked := bm.NewLockFreeBitmap(pp.numPieces)
	req := PickRequest{Bitfield: empty, BlockedPieces: blocked, PeerID: 1, NumBlocks: 1}

	require.Empty(t, pp.PickAndClaim(nil, req))
	first := pp.DebugStats()
	require.False(t, first.DiagAt.IsZero())
	require.Equal(t, 4, first.DiagSkippedBitfield)

	full := bm.NewLockFreeBitmap(pp.numPieces)
	full.Fill()
	req.Bitfield = full
	req.AllowedFast = empty
	req.Choked = true
	require.Empty(t, pp.PickAndClaim(nil, req))
	limited := pp.DebugStats()
	require.Equal(t, first.DiagAt, limited.DiagAt)
	require.Equal(t, first.DiagSkippedBitfield, limited.DiagSkippedBitfield)

	pp.mu.Lock()
	pp.lastDiagAt = time.Now().Add(-diagnosticInterval)
	pp.mu.Unlock()
	require.Empty(t, pp.PickAndClaim(nil, req))
	refreshed := pp.DebugStats()
	require.True(t, refreshed.DiagAt.After(first.DiagAt))
	require.Zero(t, refreshed.DiagSkippedBitfield)
	require.Equal(t, 4, refreshed.DiagSkippedChoked)
}

func BenchmarkPickAndClaimSparse(b *testing.B) {
	const (
		numPieces      = 1_000
		blocksPerPiece = 16
		batchSize      = 2_000
	)
	pp := newTestPicker(numPieces, blocksPerPiece)
	req := pickRequestForTest(pp, 1, batchSize)
	claims := make([]BlockClaim, 0, batchSize)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pp.AddDownloadingPiece(0)
		claims = pp.PickAndClaim(claims, req)
		if len(claims) != batchSize {
			b.Fatalf("claimed %d blocks, want %d", len(claims), batchSize)
		}
		for _, claim := range claims {
			pp.ReleaseClaim(claim)
		}
	}
}

func BenchmarkPickAndClaimNoPeerPieces(b *testing.B) {
	const numPieces = 14_000
	pp := newTestPicker(numPieces, 16)
	req := PickRequest{
		Bitfield:      bm.NewLockFreeBitmap(numPieces),
		BlockedPieces: bm.NewLockFreeBitmap(numPieces),
		PeerID:        1,
		NumBlocks:     2,
	}
	pp.PickAndClaim(nil, req) // Prime rate-limited diagnostics.

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if claims := pp.PickAndClaim(nil, req); len(claims) != 0 {
			b.Fatalf("claimed %d blocks, want none", len(claims))
		}
	}
}

func BenchmarkPickAndClaimSparseAllowedFast(b *testing.B) {
	const numPieces = 14_000
	pp := newTestPicker(numPieces, 16)
	pp.numCompletedPieces = 1 // Avoid startup mode; exercise the ordered scan.
	bitfield := bm.NewLockFreeBitmap(numPieces)
	bitfield.Fill()
	allowedFast := bm.NewLockFreeBitmap(numPieces)
	allowedFast.Set(numPieces - 1)
	req := PickRequest{
		Bitfield:      bitfield,
		AllowedFast:   allowedFast,
		BlockedPieces: bm.NewLockFreeBitmap(numPieces),
		PeerID:        1,
		NumBlocks:     1,
		Choked:        true,
	}
	claims := make([]BlockClaim, 0, 1)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		claims = pp.PickAndClaim(claims, req)
		if len(claims) != 1 {
			b.Fatalf("claimed %d blocks, want one", len(claims))
		}
		pp.ReleaseClaim(claims[0])
	}
}
