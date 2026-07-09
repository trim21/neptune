// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
)

// newRequestABlockFixture creates a Download in Downloading state with all
// pieces available in the picker (none completed). The mock peer's bitmap
// covers all pieces, and incRefcount is called so the picker knows the peer
// has them (simulating a Bitfield message).
func newRequestABlockFixture(t *testing.T, numPieces uint32) (*Download, *mockPeer) {
	t.Helper()

	d := newTestDownload(t, numPieces, 4, piece_store.NewMemStore)
	d.state.Store(uint32(Downloading))

	// Create a mock peer whose bitmap covers all pieces.
	p := newMockPeer()
	p.dl = d
	p.setNumPieces(numPieces)
	p.info = d.info
	p.bitmap.Fill()     // peer has all pieces
	p.setDesiredSize(8) // want 8 outstanding requests
	p.setChoking(false) // peer is not choking us

	// Simulate Bitfield: tell the picker the peer has all pieces.
	for i := range numPieces {
		d.picker.Load().IncRefcount(i)
	}

	return d, p
}

func TestRequestABlock_ClosedPeer(t *testing.T) {
	_, p := newRequestABlockFixture(t, 5)
	p.setClosed(true)

	p.requestABlock()

	require.Empty(t, p.enqueuedBlocks, "closed peer should not enqueue blocks")
	require.Equal(t, 0, p.sendBlockCalled, "closed peer should not call sendBlockRequests")
}

func TestRequestABlock_ChokedNoFastPieces(t *testing.T) {
	_, p := newRequestABlockFixture(t, 5)
	p.setChoking(true) // peer is choking us, no fast pieces

	p.requestABlock()

	// The picker may return zero free blocks because the peer is choking
	// and has no fast pieces. requestABlock should return without error.
	require.Equal(t, 0, p.sendBlockCalled, "choked peer with no fast pieces should not send blocks")
}

func TestRequestABlock_UnchokedPeerEnqueuesBlocks(t *testing.T) {
	_, p := newRequestABlockFixture(t, 5)
	// p is unchoked, has all 5 pieces, wants 8 requests

	p.requestABlock()
	require.NotEmpty(t, p.enqueuedBlocks, "unchoked peer should enqueue blocks")
	require.Equal(t, 1, p.sendBlockCalled, "should call SendBlockRequests once")
}

func TestRequestABlock_QueueFull(t *testing.T) {
	_, p := newRequestABlockFixture(t, 5)
	// desiredSize=8, already has 8 outstanding + 0 queued → numRequests=0
	p.setDesiredSize(8)
	p.setOutstanding(8)

	p.requestABlock()

	require.Empty(t, p.enqueuedBlocks, "full queue should not enqueue more")
	require.Equal(t, 0, p.sendBlockCalled)
}

func TestRequestABlock_QueueFullWithQueued(t *testing.T) {
	_, p := newRequestABlockFixture(t, 5)
	// desiredSize=8, outstanding=4, queued=4 → numRequests=0
	p.setDesiredSize(8)
	p.setOutstanding(4)
	p.queued = []PieceBlock{
		{PieceIndex: 0, BlockIndex: 0},
		{PieceIndex: 0, BlockIndex: 1},
		{PieceIndex: 0, BlockIndex: 2},
		{PieceIndex: 0, BlockIndex: 3},
	}

	p.requestABlock()

	require.Empty(t, p.enqueuedBlocks, "full queue should not enqueue more")
	require.Equal(t, 0, p.sendBlockCalled)
}

func TestRequestABlock_IsInQueueSkipsDuplicates(t *testing.T) {
	d, p := newRequestABlockFixture(t, 5)
	// First call: startup mode, picker returns 1 block.
	// This enters the piece into downloadingPieces (via addDownloadingPiece).
	p.requestABlock()
	require.NotEmpty(t, p.enqueuedBlocks, "first call should enqueue a startup block")

	// Record what block was enqueued and add it to in-queue set.
	firstBlock := p.enqueuedBlocks[0]
	ch := pieceChunk(d.info, firstBlock.PieceIndex, firstBlock.BlockIndex)
	p.addToQueue(ch)

	// Now the piece is in downloadingPieces. Second call enters partial pieces
	// path and returns remaining free blocks (blocks 1, 2, 3) plus busy blocks.
	// But "free" here means blockStateNone. Since only block 0 is enqueued
	// (not yet sent, not marked in picker), all blocks are still blockStateNone.
	// The picker will return them all as free, and IsInQueue skips block 0.
	enqueuedBefore := len(p.enqueuedBlocks)
	p.requestABlock()

	// Should have enqueued new blocks (skipping the one already in queue).
	require.Greater(t, len(p.enqueuedBlocks), enqueuedBefore,
		"should enqueue additional blocks beyond the startup block")
	// Block 0 should not appear again.
	for _, b := range p.enqueuedBlocks[1:] {
		if b.PieceIndex == firstBlock.PieceIndex && b.BlockIndex == firstBlock.BlockIndex {
			t.Error("duplicate block enqueued, IsInQueue should have skipped it")
		}
	}
}

func TestRequestABlock_CompletedPieceSkipped(t *testing.T) {
	// Mark piece 0 as complete — blocks for piece 0 should be skipped.
	d, p := newRequestABlockFixture(t, 3)
	d.completedBm.Set(0)
	d.picker.Load().WeHave(0, d.info)

	// Only pieces 1, 2 are available in picker now.
	// Peer bitmap has all pieces.
	p.requestABlock()

	// Verify no blocks for piece 0 were enqueued
	for _, b := range p.enqueuedBlocks {
		require.NotEqual(t, uint32(0), b.PieceIndex, "blocks for completed piece should be skipped")
	}
}

func TestRequestABlock_EndgameBusyBlocks(t *testing.T) {
	d, p := newRequestABlockFixture(t, 5)

	// First call: startup mode — enters one piece into downloadingPieces.
	p.requestABlock()
	firstPiece := p.enqueuedBlocks[0].PieceIndex

	// Add a DIFFERENT piece (piece 0) to downloading, then mark blocks 1-3
	// as requested. Block 0 stays free → this piece has 1 free + 3 busy blocks.
	// (If startup picked piece 0, use piece 4 instead.)
	busyPiece := uint32(0)
	if firstPiece == 0 {
		busyPiece = 4
	}
	picker := d.picker.Load()
	picker.AddDownloadingPiece(busyPiece, d.info)
	nb := d.info.PieceBlockCount(busyPiece)
	for bi := 1; bi < nb; bi++ {
		picker.MarkAsRequesting(busyPiece, bi)
	}

	// Reset tracking
	p.enqueuedBlocks = p.enqueuedBlocks[:0]
	p.queued = p.queued[:0]
	p.setDesiredSize(100)

	p.requestABlock()

	// Piece with 1 free + 3 busy blocks should appear in partial pieces path.
	require.NotEmpty(t, p.lastPickRes.BusyBlocks, "should have busy blocks from mixed piece")
	// One busy block is enqueued per endgame call.
	require.NotEmpty(t, p.enqueuedBlocks, "should enqueue at least 1 busy block in endgame")
}

func TestRequestABlock_AllPiecesCompleted(t *testing.T) {
	d, p := newRequestABlockFixture(t, 3)
	d.completedBm.Fill() // all pieces done

	p.requestABlock()

	require.Empty(t, p.enqueuedBlocks, "no blocks when all pieces completed")
	require.Equal(t, 0, p.sendBlockCalled)
}

func TestRequestABlock_HasPartialCapacity(t *testing.T) {
	// First call: startup mode returns 1 block (adds piece to downloadingPieces).
	// Second call: partial pieces path returns all remaining free blocks.
	_, p := newRequestABlockFixture(t, 5)
	p.setDesiredSize(8)
	p.setOutstanding(3) // numRequests = 8 - 3 - 0 = 5

	// First call — startup mode, 1 block
	p.requestABlock()
	require.Len(t, p.enqueuedBlocks, 1, "startup mode returns exactly 1 block")

	// Prepare for second call: simulate that the startup block was sent.
	p.setOutstanding(3 + 1) // startup block now outstanding

	// Second call — partial pieces path, fills remaining capacity
	p.requestABlock()
	// Should have enqueued additional blocks up to capacity.
	require.GreaterOrEqual(t, len(p.enqueuedBlocks), 2,
		"should enqueue additional blocks on second call")
}

func TestRequestABlock_ChokedWithFastPieces(t *testing.T) {
	// Peer is choking, but piece 0 is in allowed-fast set.
	// The picker should return only blocks from allowed-fast pieces.
	t.Skip("TODO: requires picker to handle allowed-fast during choked state")
}
