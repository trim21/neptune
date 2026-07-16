// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"neptune/internal/pkg/bm"
)

// RequestABlock picks blocks for a peer using this piece picker.
// The caller must ensure the download is in downloading state and
// completedBm.Count() < info.NumPieces.
//
// last is the previous PickResult; its FreeBlocks/BusyBlocks backing arrays
// are reused to avoid allocation. The idiom mirrors append:
//
//	result = pp.RequestABlock(result, desiredQueueSize, outstanding, queued, choked, peerBitfield, fastBitmap)
//
// The returned PickResult has FreeBlocks/BusyBlocks containing only usable
// (unfinished, not completed, not chunk-done) blocks.
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
	if pp == nil {
		return last
	}

	last.FreeBlocks = last.FreeBlocks[:0]
	last.BusyBlocks = last.BusyBlocks[:0]

	if pp.missingBm.Count() == 0 {
		return last
	}

	numRequests := desiredQueueSize - outstanding - queued
	if numRequests <= 0 {
		return last
	}

	fastBm := fastBitmap
	if fastBm == nil {
		fastBm = bm.New(0)
	}

	last = pp.PickPieces(
		peerBitfield,
		choked,
		fastBm,
		blockedPieces,
		numRequests,
		0,
		nil,
		onParole,
		peerID,
		last,
	)

	if len(last.FreeBlocks) == 0 && choked && fastBm.Count() == 0 {
		return last
	}

	// In-place filter: keep only blocks that are not finished, not chunk-done.
	n := 0
	for _, fb := range last.FreeBlocks {
		if pp.IsFinished(fb.PieceIndex, fb.BlockIndex) {
			continue
		}
		chunkPi := fb.PieceIndex*pp.blocksPerPiece + uint32(fb.BlockIndex)
		if pp.chunkDoneBm != nil && pp.chunkDoneBm.Contains(chunkPi) {
			continue
		}
		last.FreeBlocks[n] = fb
		n++
	}
	last.FreeBlocks = last.FreeBlocks[:n]

	m := 0
	for _, bb := range last.BusyBlocks {
		chunkPi := bb.PieceIndex*pp.blocksPerPiece + uint32(bb.BlockIndex)
		if pp.chunkDoneBm != nil && pp.chunkDoneBm.Contains(chunkPi) {
			continue
		}
		last.BusyBlocks[m] = bb
		m++
	}
	last.BusyBlocks = last.BusyBlocks[:m]

	// Diagnostic: if post-filter emptied the result, record why.
	if len(last.FreeBlocks) == 0 && len(last.BusyBlocks) == 0 && numRequests > 0 {
		pp.recordDiag(peerBitfield, choked, fastBm, blockedPieces, true)
	}

	return last
}
