// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"sync"

	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

var legacyTestClaims = struct {
	m map[*PiecePicker]map[PieceBlock][]BlockClaim
	sync.Mutex
}{m: make(map[*PiecePicker]map[PieceBlock][]BlockClaim)}

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
	info := testInfo(numPieces, blocksPerPiece)
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	missingBm.Fill()
	chunkDoneBm := bm.New(info.NumPieces * blocksPerPiece)

	return NewPiecePicker(info, missingBm, chunkDoneBm, new(atomic.Uint32))
}

func testBlockIndex(v any) uint32 {
	switch value := v.(type) {
	case int:
		return uint32(value)
	case uint32:
		return value
	default:
		panic("unsupported block index")
	}
}

// The legacy helpers below exist only to keep strategy regression tests
// focused on selection order. Production code cannot perform anonymous state
// transitions because these methods are compiled only into this test package.
func (pp *PiecePicker) MarkAsRequesting(pieceIndex uint32, blockIndex any) bool {
	return pp.TryMarkAsRequesting(pieceIndex, blockIndex, false)
}

func (pp *PiecePicker) TryMarkAsRequesting(pieceIndex uint32, blockIndex any, retry bool) bool {
	block := PieceBlock{PieceIndex: pieceIndex, BlockIndex: testBlockIndex(blockIndex)}
	pp.mu.Lock()
	claim, ok := pp.registerClaimUnsafe(block, pp.nextClaimToken+1, retry)
	pp.mu.Unlock()
	if !ok {
		return false
	}
	legacyTestClaims.Lock()
	claims := legacyTestClaims.m[pp]
	if claims == nil {
		claims = make(map[PieceBlock][]BlockClaim)
		legacyTestClaims.m[pp] = claims
	}
	claims[block] = append(claims[block], claim)
	legacyTestClaims.Unlock()
	return true
}

func (pp *PiecePicker) MarkAsResponded(pieceIndex uint32, blockIndex any) {
	block := PieceBlock{PieceIndex: pieceIndex, BlockIndex: testBlockIndex(blockIndex)}
	claim, ok := popLegacyTestClaim(pp, block)
	if ok {
		pp.AcceptResponse(claim)
	}
}

func (pp *PiecePicker) AbortDownload(pieceIndex uint32, blockIndex any) {
	block := PieceBlock{PieceIndex: pieceIndex, BlockIndex: testBlockIndex(blockIndex)}
	claim, ok := popLegacyTestClaim(pp, block)
	if ok {
		pp.ReleaseClaim(claim)
	}
}

func popLegacyTestClaim(pp *PiecePicker, block PieceBlock) (BlockClaim, bool) {
	legacyTestClaims.Lock()
	defer legacyTestClaims.Unlock()
	claims := legacyTestClaims.m[pp][block]
	if len(claims) == 0 {
		return BlockClaim{}, false
	}
	claim := claims[0]
	legacyTestClaims.m[pp][block] = claims[1:]
	return claim, true
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
