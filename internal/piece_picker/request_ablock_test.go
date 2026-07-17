// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/bm"
)

func TestRequestABlock_AllCompleted(t *testing.T) {
	pp := newTestPicker(3, 4)
	pp.missingBm.Clear()

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, false, nil, nil, bm.NewLockFreeBitmap(3), false, 0)

	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_QueueFull(t *testing.T) {
	pp := newTestPicker(5, 4)
	result := pp.RequestABlock(PickResult{}, 8, 8, 0, false, nil, nil, bm.NewLockFreeBitmap(5), false, 0)
	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_QueueFullWithQueued(t *testing.T) {
	pp := newTestPicker(5, 4)
	result := pp.RequestABlock(PickResult{}, 8, 4, 4, false, nil, nil, bm.NewLockFreeBitmap(5), false, 0)
	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_ChokedNoFast(t *testing.T) {
	pp := newTestPicker(5, 4)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()
	for i := range pp.info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, true, peerBitfield, nil, bm.NewLockFreeBitmap(5), false, 0)
	require.Empty(t, result.FreeBlocks, "choked + no fast should return no free blocks")
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_ChokedWithFast(t *testing.T) {
	pp := newTestPicker(5, 4)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()

	fastBm := bm.New(pp.info.NumPieces)
	fastBm.Set(0)

	for i := range pp.info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, true, peerBitfield, fastBm, bm.NewLockFreeBitmap(5), false, 0)
	require.NotEmpty(t, result.FreeBlocks, "fast piece should allow blocks through choke")
	for _, fb := range result.FreeBlocks {
		require.Equal(t, uint32(0), fb.PieceIndex, "only allowed-fast piece should be returned")
	}
}

func TestRequestABlock_UnchokedNormal(t *testing.T) {
	pp := newTestPicker(5, 4)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()
	for i := range pp.info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, false, peerBitfield, nil, bm.NewLockFreeBitmap(5), false, 0)
	require.NotEmpty(t, result.FreeBlocks, "unchoked peer should get free blocks")
}

func TestLateReleaseDoesNotRollbackRespondedBlock(t *testing.T) {
	pp := newTestPicker(1, 4)
	peer := newPickerTestPeer(t, pp, 1)
	claim := peer.claimBlock(t, 0, 0)

	require.True(t, peer.accept(claim))
	require.False(t, peer.release(claim))

	require.True(t, pp.IsFinished(0, 0), "late timeout must not turn a responded block back into free")
	require.Equal(t, 0, pp.DebugStats().DownloadQueue)
}

func TestReleaseClaimKeepsOtherEndgameRequest(t *testing.T) {
	pp := newTestPicker(1, 4)
	firstPeer := newPickerTestPeer(t, pp, 1)
	firstClaims := firstPeer.pickPiece(0, 4)
	require.Len(t, firstClaims, 4)
	secondPeer := newPickerTestPeer(t, pp, 2)
	secondClaims := secondPeer.pickPiece(0, 1)
	require.Len(t, secondClaims, 1)
	duplicate := secondClaims[0]

	var original BlockClaim
	for _, claim := range firstClaims {
		if claim.Block == duplicate.Block {
			original = claim
			continue
		}
		require.True(t, firstPeer.release(claim))
	}
	require.NotEqual(t, BlockClaim{}, original)

	require.True(t, firstPeer.release(original))
	stats := pp.DebugStats()
	require.Equal(t, 1, stats.RequestedBlocks)
	require.Equal(t, 1, stats.DownloadQueue)

	require.True(t, secondPeer.release(duplicate))
	stats = pp.DebugStats()
	require.Equal(t, 0, stats.RequestedBlocks)
	require.Equal(t, 0, stats.DownloadQueue)
}

func TestStalePickCannotRollbackRespondedBlock(t *testing.T) {
	pp := newTestPicker(1, 4)
	peerBitfield := bm.New(1)
	peerBitfield.Fill()

	result := pp.RequestABlock(PickResult{}, 1, 0, 0, false, peerBitfield, nil, bm.NewLockFreeBitmap(1), false, 1)
	require.Len(t, result.FreeBlocks, 1)
	block := result.FreeBlocks[0]
	peer := newPickerTestPeer(t, pp, 1)
	claim := peer.claimBlock(t, block.PieceIndex, block.BlockIndex)

	require.True(t, peer.accept(claim))
	otherPeer := newPickerTestPeer(t, pp, 2)
	for _, other := range otherPeer.pickPiece(block.PieceIndex, pp.info.PieceBlockCount(block.PieceIndex)) {
		require.NotEqual(t, block, other.Block)
		otherPeer.release(other)
	}
	require.True(t, pp.IsFinished(block.PieceIndex, block.BlockIndex))
}

func TestRequestABlock_LastPickResultReuse(t *testing.T) {
	pp := newTestPicker(5, 4)

	peerBitfield := bm.New(pp.info.NumPieces)
	peerBitfield.Fill()
	for i := range pp.info.NumPieces {
		pp.IncRefcount(i)
	}

	last := PickResult{
		FreeBlocks: make([]PieceBlock, 0, 16),
		BusyBlocks: make([]PieceBlock, 0, 16),
	}

	result := pp.RequestABlock(last, 8, 0, 0, false, peerBitfield, nil, bm.NewLockFreeBitmap(5), false, 0)
	require.NotEmpty(t, result.FreeBlocks)

	require.Equal(t, 16, cap(result.FreeBlocks), "FreeBlocks backing array should be preserved")
	require.Equal(t, 16, cap(result.BusyBlocks), "BusyBlocks backing array should be preserved")
}
