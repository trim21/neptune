// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"neptune/internal/pkg/assert"
)

// PickAndClaim selects blocks and records peer ownership in one critical
// section. Every returned claim must be consumed by AcceptResponse,
// ReleaseClaim, ReleasePeerClaims, or ReleaseAllClaims.
func (pp *PiecePicker) PickAndClaim(reuse []BlockClaim, req PickRequest) []BlockClaim {
	reuse = reuse[:0]
	if pp == nil || req.NumBlocks <= 0 || !pp.requestGate.enabled() {
		return reuse
	}

	pp.mu.Lock()
	defer pp.mu.Unlock()

	if pp.missingBm.Count() == 0 {
		return reuse
	}

	bitfield := req.Bitfield
	if bitfield == nil {
		bitfield = pp.emptyPiecesBm
	}
	allowedFast := req.AllowedFast
	if allowedFast == nil {
		allowedFast = pp.emptyPiecesBm
	}
	blockedPieces := req.BlockedPieces
	if blockedPieces == nil {
		blockedPieces = pp.emptyPiecesBm
	}

	result := pp.pickPiecesUnsafe(
		bitfield,
		req.Choked,
		allowedFast,
		blockedPieces,
		req.NumBlocks,
		req.PreferContiguous,
		req.SuggestedPieces,
		req.OnParole,
		req.PeerID,
		pp.pickScratch,
	)
	pp.pickScratch = result

	for _, block := range result.FreeBlocks {
		if len(reuse) >= req.NumBlocks || pp.chunkAlreadyDoneUnsafe(block) {
			continue
		}
		claim, ok := pp.registerClaimUnsafe(block, req.PeerID, false)
		if ok {
			reuse = append(reuse, claim)
		}
	}

	// Preserve the existing endgame behavior: only duplicate when no free
	// block was claimed, and create at most one duplicate per scheduling pass.
	if len(reuse) == 0 {
		for _, block := range result.BusyBlocks {
			if pp.chunkAlreadyDoneUnsafe(block) {
				continue
			}
			claim, ok := pp.registerClaimUnsafe(block, req.PeerID, true)
			if ok {
				reuse = append(reuse, claim)
				break
			}
		}
	}

	return reuse
}

func (pp *PiecePicker) chunkAlreadyDoneUnsafe(block PieceBlock) bool {
	idx := pp.blockInfoIdx(block.PieceIndex) + int(block.BlockIndex)
	if pp.blockInfos.get(idx) == blockStateResponded {
		return true
	}
	chunkIdx := pp.chunkIndex(block.PieceIndex, int(block.BlockIndex))
	return pp.chunkDoneBm != nil && pp.chunkDoneBm.Contains(chunkIdx)
}

func (pp *PiecePicker) registerClaimUnsafe(block PieceBlock, owner uint64, retry bool) (BlockClaim, bool) {
	if block.PieceIndex >= pp.numPieces || block.BlockIndex >= uint32(pp.numBlocksInPiece(block.PieceIndex)) {
		return BlockClaim{}, false
	}

	idx := pp.blockInfoIdx(block.PieceIndex) + int(block.BlockIndex)
	state := pp.blockInfos.get(idx)
	if retry {
		if state != blockStateRequested {
			return BlockClaim{}, false
		}
	} else if state != blockStateNone {
		return BlockClaim{}, false
	}

	chunkIdx := pp.chunkIndex(block.PieceIndex, int(block.BlockIndex))
	claims := pp.claimsByBlock[chunkIdx]
	if retry {
		assert.NotEqual(claims.count, uint8(0), "requested block has no named claim")
	} else {
		assert.Equal(claims.count, uint8(0), "free block already has named claims")
	}
	if claims.count >= maxClaimsPerBlock {
		return BlockClaim{}, false
	}
	for i := range int(claims.count) {
		record := pp.activeClaims[claims.tokens[i]]
		if record.owner == owner {
			return BlockClaim{}, false
		}
	}

	pp.nextClaimToken++
	if pp.nextClaimToken == 0 {
		pp.nextClaimToken++
	}
	token := pp.nextClaimToken
	claim := BlockClaim{Block: block, token: token}
	pp.activeClaims[token] = claimRecord{block: block, owner: owner}
	claims.tokens[claims.count] = token
	claims.count++
	pp.claimsByBlock[chunkIdx] = claims
	peerClaims := pp.claimsByPeer[owner]
	if peerClaims == nil {
		peerClaims = make(map[uint64]struct{})
		pp.claimsByPeer[owner] = peerClaims
	}
	peerClaims[token] = struct{}{}

	if state == blockStateNone {
		pp.blockInfos.set(idx, blockStateRequested)
		pp.downloadQueueSize++
		pp.freeBlocks--
		pp.addDownloadingPieceUnsafe(block.PieceIndex)
		if dp := pp.findDownloadingPiece(block.PieceIndex); dp != nil {
			dp.requested++
		}
	}
	if _, ok := pp.parolePieceOwners[block.PieceIndex]; !ok {
		pp.parolePieceOwners[block.PieceIndex] = owner
	}
	return claim, true
}
