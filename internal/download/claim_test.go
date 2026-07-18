// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/bm"
)

func claimBlockForTest(t testing.TB, d *Download, pieceIndex, blockIndex uint32, peerID uint64) BlockClaim {
	t.Helper()
	d.picker.Load().AddDownloadingPiece(pieceIndex)
	bitfield := bm.NewLockFreeBitmap(d.info.NumPieces)
	bitfield.Set(pieceIndex)
	claims := d.picker.Load().PickAndClaim(nil, PickRequest{
		Bitfield:      bitfield,
		BlockedPieces: bm.NewLockFreeBitmap(d.info.NumPieces),
		PeerID:        peerID,
		NumBlocks:     d.info.PieceBlockCount(pieceIndex),
	})
	var wanted BlockClaim
	for _, claim := range claims {
		if claim.Block.PieceIndex == pieceIndex && claim.Block.BlockIndex == blockIndex {
			wanted = claim
			continue
		}
		d.picker.Load().ReleaseClaim(claim)
	}
	require.NotZero(t, wanted, "picker did not claim piece=%d block=%d", pieceIndex, blockIndex)
	return wanted
}

func claimedSubmitForTest(t testing.TB, d *Download, submit chunkSubmit) chunkSubmit {
	t.Helper()
	if submit.claim != (BlockClaim{}) {
		return submit
	}
	blockIndex := submit.res.Begin / uint32(defaultBlockSize)
	submit.claim = claimBlockForTest(t, d, submit.res.PieceIndex, blockIndex, submit.peerID)
	return submit
}
