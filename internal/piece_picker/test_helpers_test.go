// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"testing"

	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

// testInfo builds a minimal meta.Info for testing with the given number of
// pieces and blocks per piece.
func testInfo(numPieces, blocksPerPiece uint32) meta.Info {
	pieceLen := int64(blocksPerPiece) * meta.DefaultBlockSize
	return meta.Info{
		NumPieces:     numPieces,
		PieceLength:   pieceLen,
		LastPieceSize: pieceLen,
	}
}

// newTestPicker creates a fully initialized PiecePicker for testing.
// All pieces are wanted, none completed.
func newTestPicker(numPieces, blocksPerPiece uint32) *PiecePicker {
	pp, _ := newTestPickerWithState(numPieces, blocksPerPiece)
	return pp
}

func newTestPickerWithState(numPieces, blocksPerPiece uint32) (*PiecePicker, *atomic.Uint32) {
	info := testInfo(numPieces, blocksPerPiece)
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	missingBm.Fill()
	chunkDoneBm := bm.New(info.NumPieces * blocksPerPiece)
	state := atomic.NewUint32(1)

	return NewPiecePicker(
		info,
		missingBm,
		chunkDoneBm,
		new(atomic.Uint32),
		NewRequestGate(state, 1),
	), state
}

type pickerTestPeer struct {
	pp    *PiecePicker
	owned map[uint64]BlockClaim
	id    uint64
}

func newPickerTestPeer(t testing.TB, pp *PiecePicker, id uint64) *pickerTestPeer {
	t.Helper()
	p := &pickerTestPeer{pp: pp, owned: make(map[uint64]BlockClaim), id: id}
	t.Cleanup(func() {
		pp.ReleasePeerClaims(id)
	})
	return p
}

func (p *pickerTestPeer) pick(req PickRequest) []BlockClaim {
	req.PeerID = p.id
	claims := p.pp.PickAndClaim(nil, req)
	for _, claim := range claims {
		p.owned[claim.token] = claim
	}
	return claims
}

func (p *pickerTestPeer) pickPiece(pieceIndex uint32, numBlocks int) []BlockClaim {
	bitfield := bm.New(p.pp.info.NumPieces)
	bitfield.Set(pieceIndex)
	claims := make([]BlockClaim, 0, numBlocks)
	for len(claims) < numBlocks {
		picked := p.pick(PickRequest{
			Bitfield:      bitfield,
			BlockedPieces: bm.NewLockFreeBitmap(p.pp.info.NumPieces),
			NumBlocks:     numBlocks - len(claims),
		})
		if len(picked) == 0 {
			break
		}
		claims = append(claims, picked...)
	}
	return claims
}

func (p *pickerTestPeer) claimBlock(t testing.TB, pieceIndex, blockIndex uint32) BlockClaim {
	t.Helper()
	claims := p.pickPiece(pieceIndex, p.pp.info.PieceBlockCount(pieceIndex))
	var selected BlockClaim
	for _, claim := range claims {
		if claim.Block.BlockIndex == blockIndex {
			selected = claim
			continue
		}
		p.release(claim)
	}
	if selected != (BlockClaim{}) {
		return selected
	}
	t.Fatalf("picker did not claim piece=%d block=%d", pieceIndex, blockIndex)
	return BlockClaim{}
}

func (p *pickerTestPeer) accept(claim BlockClaim) bool {
	delete(p.owned, claim.token)
	return p.pp.AcceptResponse(claim)
}

func (p *pickerTestPeer) release(claim BlockClaim) bool {
	delete(p.owned, claim.token)
	return p.pp.ReleaseClaim(claim)
}

func respondPieceForTest(t testing.TB, pp *PiecePicker, pieceIndex uint32, peerID uint64) {
	t.Helper()
	p := newPickerTestPeer(t, pp, peerID)
	claims := p.pickPiece(pieceIndex, pp.info.PieceBlockCount(pieceIndex))
	if len(claims) != pp.info.PieceBlockCount(pieceIndex) {
		t.Fatalf("picker claimed %d blocks for piece %d, want %d", len(claims), pieceIndex, pp.info.PieceBlockCount(pieceIndex))
	}
	for _, claim := range claims {
		if !p.accept(claim) {
			t.Fatalf("picker rejected live claim %+v", claim.Block)
		}
	}
}

func (pp *PiecePicker) PickPieces(
	bitfield *bm.Bitmap,
	choked bool,
	allowedFast *bm.Bitmap,
	blockedPieces *bm.LockFreeBitmap,
	numBlocks int,
	preferContiguous int,
	suggestedPieces []uint32,
	onParole bool,
	peerID uint64,
	result PickResult,
) PickResult {
	if allowedFast == nil {
		allowedFast = bm.New(0)
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.pickPiecesUnsafe(bitfield, choked, allowedFast, blockedPieces, numBlocks, preferContiguous, suggestedPieces, onParole, peerID, result)
}

func (pp *PiecePicker) RequestABlock(
	last PickResult,
	desiredQueueSize int,
	outstanding int,
	queued int,
	choked bool,
	peerBitfield *bm.Bitmap,
	fastBitmap *bm.Bitmap,
	blockedPieces *bm.LockFreeBitmap,
	onParole bool,
	peerID uint64,
) PickResult {
	last.FreeBlocks = last.FreeBlocks[:0]
	last.BusyBlocks = last.BusyBlocks[:0]
	numRequests := desiredQueueSize - outstanding - queued
	if numRequests <= 0 || pp.missingBm.Count() == 0 {
		return last
	}
	if fastBitmap == nil {
		fastBitmap = bm.New(0)
	}
	return pp.PickPieces(peerBitfield, choked, fastBitmap, blockedPieces, numRequests, 0, nil, onParole, peerID, last)
}
