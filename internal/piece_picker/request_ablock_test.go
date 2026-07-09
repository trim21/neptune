// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

func testInfo(numPieces uint32) meta.Info {
	return meta.Info{
		NumPieces:   numPieces,
		PieceLength: 4 * 16384, // 4 blocks per piece
	}
}

func newTestPicker(numPieces uint32) *PiecePicker {
	info := testInfo(numPieces)
	completedBm := bm.New(info.NumPieces)
	wantedBm := bm.New(info.NumPieces)
	return NewPiecePicker(info, completedBm, wantedBm, nil, nil, false)
}

func TestRequestABlock_AllCompleted(t *testing.T) {
	info := testInfo(3)
	completedBm := bm.New(info.NumPieces)
	completedBm.Fill()

	pp := NewPiecePicker(info, completedBm, nil, nil, nil, false)
	result := pp.RequestABlock(PickResult{}, 8, 0, 0, false, nil, nil)

	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_QueueFull(t *testing.T) {
	pp := newTestPicker(5)

	// desired=8, outstanding=8, queued=0 → numRequests=0
	result := pp.RequestABlock(PickResult{}, 8, 8, 0, false, nil, nil)
	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_QueueFullWithQueued(t *testing.T) {
	pp := newTestPicker(5)

	// desired=8, outstanding=4, queued=4 → numRequests=0
	result := pp.RequestABlock(PickResult{}, 8, 4, 4, false, nil, nil)
	require.Empty(t, result.FreeBlocks)
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_ChokedNoFast(t *testing.T) {
	numPieces := uint32(5)
	info := testInfo(numPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()

	peerBitfield := bm.New(info.NumPieces)
	peerBitfield.Fill()

	pp := NewPiecePicker(info, bm.New(info.NumPieces), wantedBm, nil, nil, false)

	for i := range info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, true, peerBitfield, nil)
	require.Empty(t, result.FreeBlocks, "choked + no fast should return no free blocks")
	require.Empty(t, result.BusyBlocks)
}

func TestRequestABlock_ChokedWithFast(t *testing.T) {
	numPieces := uint32(5)
	info := testInfo(numPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()

	peerBitfield := bm.New(info.NumPieces)
	peerBitfield.Fill()

	fastBm := bm.New(info.NumPieces)
	fastBm.Set(0)

	pp := NewPiecePicker(info, bm.New(info.NumPieces), wantedBm, nil, nil, false)

	for i := range info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, true, peerBitfield, fastBm)
	require.NotEmpty(t, result.FreeBlocks, "fast piece should allow blocks through choke")
	for _, fb := range result.FreeBlocks {
		require.Equal(t, uint32(0), fb.PieceIndex, "only allowed-fast piece should be returned")
	}
}

func TestRequestABlock_UnchokedNormal(t *testing.T) {
	numPieces := uint32(5)
	info := testInfo(numPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()

	peerBitfield := bm.New(info.NumPieces)
	peerBitfield.Fill()

	pp := NewPiecePicker(info, bm.New(info.NumPieces), wantedBm, nil, nil, false)

	for i := range info.NumPieces {
		pp.IncRefcount(i)
	}

	result := pp.RequestABlock(PickResult{}, 8, 0, 0, false, peerBitfield, nil)
	require.NotEmpty(t, result.FreeBlocks, "unchoked peer should get free blocks")
}

func TestRequestABlock_LastPickResultReuse(t *testing.T) {
	numPieces := uint32(5)
	info := testInfo(numPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()

	peerBitfield := bm.New(info.NumPieces)
	peerBitfield.Fill()

	pp := NewPiecePicker(info, bm.New(info.NumPieces), wantedBm, nil, nil, false)

	for i := range info.NumPieces {
		pp.IncRefcount(i)
	}

	last := PickResult{
		FreeBlocks: make([]PieceBlock, 0, 16),
		BusyBlocks: make([]PieceBlock, 0, 16),
	}

	result := pp.RequestABlock(last, 8, 0, 0, false, peerBitfield, nil)
	require.NotEmpty(t, result.FreeBlocks)

	require.Equal(t, 16, cap(result.FreeBlocks), "FreeBlocks backing array should be preserved")
	require.Equal(t, 16, cap(result.BusyBlocks), "BusyBlocks backing array should be preserved")
}
