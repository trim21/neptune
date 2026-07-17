// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/bm"
)

func pickRequestForTest(pp *PiecePicker, peerID uint64, numBlocks int) PickRequest {
	bitfield := bm.New(pp.info.NumPieces)
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

func TestDisableAndResetInvalidateClaimsWithoutReusingTokens(t *testing.T) {
	pp := newTestPicker(2, 4)
	pp.AddDownloadingPiece(0)
	claims := pp.PickAndClaim(nil, pickRequestForTest(pp, 7, 4))
	require.Len(t, claims, 4)
	lastToken := claims[len(claims)-1].token
	require.Equal(t, 4, pp.DisableRequests())
	require.Empty(t, pp.PickAndClaim(nil, pickRequestForTest(pp, 8, 1)))
	require.Zero(t, pp.DebugStats().ActiveClaims)

	pp.ResetAll()
	pp.EnableRequests()
	pp.AddDownloadingPiece(0)
	claim := pp.PickAndClaim(nil, pickRequestForTest(pp, 7, 1))[0]
	require.Greater(t, claim.token, lastToken)
	require.False(t, pp.ReleaseClaim(claims[0]))
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
