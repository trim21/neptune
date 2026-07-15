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

func TestAbortDownloadDoesNotRollbackRespondedBlock(t *testing.T) {
	pp := newTestPicker(1, 4)

	require.True(t, pp.MarkAsRequesting(0, 0))
	pp.MarkAsResponded(0, 0)
	pp.AbortDownload(0, 0)

	require.True(t, pp.IsFinished(0, 0), "late timeout must not turn a responded block back into free")
	require.Equal(t, 0, pp.DebugStats().DownloadQueue)
}

func TestAbortDownloadKeepsOtherEndgameRequest(t *testing.T) {
	pp := newTestPicker(1, 4)

	require.True(t, pp.MarkAsRequesting(0, 0))
	require.True(t, pp.TryMarkAsRequesting(0, 0, true))

	pp.AbortDownload(0, 0)
	stats := pp.DebugStats()
	require.Equal(t, 1, stats.RequestedBlocks)
	require.Equal(t, 1, stats.DownloadQueue)

	pp.AbortDownload(0, 0)
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

	require.True(t, pp.TryMarkAsRequesting(block.PieceIndex, block.BlockIndex, false))
	pp.MarkAsResponded(block.PieceIndex, block.BlockIndex)
	require.False(t, pp.TryMarkAsRequesting(block.PieceIndex, block.BlockIndex, false))
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
