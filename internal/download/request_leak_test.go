// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// TestDroppedResponseReleasesPickerClaim reproduces the stall where a
// download gets stuck with every remaining block in Requested state:
// the peer read loop consumes the request from myRequests (resIsValid),
// but the response reaches backgroundResHandler after the download left
// Downloading state (Stop / queue demote / Moving). Dropping the response
// must still release the picker claim, otherwise the block stays Requested
// forever and no peer can ever pick it again.
func TestDroppedResponseReleasesPickerClaim(t *testing.T) {
	const numPieces = 2
	const blocksPerPiece = 4

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

	picker := d.picker.Load()
	claim := claimBlockForTest(t, d, 0, 0, 1)
	require.Equal(t, 1, picker.DebugStats().DownloadQueue)

	// Download leaves Downloading before the in-flight response is handled.
	_, err := d.transition(Stopped)
	require.NoError(t, err)

	d.resChan = make(chan chunkSubmit, 1)
	go d.backgroundResHandler()

	d.resChan <- chunkSubmit{
		res: &proto.ChunkResponse{
			PieceIndex: 0, Begin: 0,
			Data: make([]byte, defaultBlockSize),
		},
		peerID: 1,
		claim:  claim,
	}

	require.Eventually(t, func() bool {
		return picker.DebugStats().DownloadQueue == 0
	}, 2*time.Second, 5*time.Millisecond, "dropped response must release the picker claim")

	st := picker.DebugStats()
	require.Equal(t, 0, st.RequestedBlocks)
	require.Equal(t, numPieces*blocksPerPiece, st.FreeBlocks)
	require.False(t, picker.IsFinished(0, 0), "block must be free again, not responded")
}

// TestAbortAllRequestsLockedReleasesClaims covers the snub path: a snubbed
// peer discards both its in-flight requests and its (not yet sent) request
// queue. Queued blocks were already marked Requested in the picker when
// picked, so clearing the queue without aborting leaks them permanently.
func TestAbortAllRequestsLockedReleasesClaims(t *testing.T) {
	const numPieces = 2
	const blocksPerPiece = 4

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
	picker := d.picker.Load()

	p := &peerImpl{
		peerCtx:    d.newPeerContext(),
		log:        zerolog.Nop(),
		myRequests: xsync.NewMap[proto.ChunkRequest, trackedRequest](),
		id:         1,
	}

	// One block in flight (on the wire, tracked in myRequests).
	claim0 := claimBlockForTest(t, d, 0, 0, p.id)
	p.myRequests.Store(pieceChunk(d.info, 0, 0), trackedRequest{claim: claim0, sentAt: time.Now()})

	// One block queued locally (marked Requested, not yet sent).
	claim1 := claimBlockForTest(t, d, 0, 1, p.id)
	require.True(t, p.requestQueue.Push(claim1))

	p.requestMu.Lock()
	p.abortAllRequestsUnsafe()
	p.requestMu.Unlock()

	st := picker.DebugStats()
	require.Equal(t, 0, st.DownloadQueue)
	require.Equal(t, 0, st.RequestedBlocks)
	require.Equal(t, numPieces*blocksPerPiece, st.FreeBlocks)
	require.Zero(t, p.myRequests.Size())
	require.Zero(t, p.requestQueue.Len())
}

func TestLeavingDownloadingClearsNamedClaims(t *testing.T) {
	states := []State{Stopped, Seeding, Checking, Moving, Error}
	for _, state := range states {
		t.Run(state.String(), func(t *testing.T) {
			d := newTestDownload(t, 2, 4, piece_store.NewMemStore)
			peer := newMockPeer()
			peer.dl = d
			peer.info = d.info
			peer.setNumPieces(d.info.NumPieces)
			peer.bitmap.Fill()
			d.peers.Store(peer.ID(), peer)

			claim := claimBlockForTest(t, d, 0, 0, peer.ID())
			peer.queued = append(peer.queued, claim)
			_, err := d.transition(state)
			require.NoError(t, err)

			stats := d.picker.Load().DebugStats()
			require.Zero(t, stats.ActiveClaims)
			require.Zero(t, stats.RequestedBlocks)
			require.Empty(t, peer.queued)
			require.Empty(t, d.picker.Load().PickAndClaim(nil, PickRequest{PeerID: 2, NumBlocks: 1}))
		})
	}
}

func TestPendingDownloadingAcceptsInflightResponse(t *testing.T) {
	d := newTestDownload(t, 2, 4, piece_store.NewMemStore)
	picker := d.picker.Load()
	claim := claimBlockForTest(t, d, 0, 0, 1)

	_, err := d.transition(PendingDownloading)
	require.NoError(t, err)
	require.Equal(t, 1, picker.DebugStats().ActiveClaims)

	peerPieces := bm.New(d.info.NumPieces)
	peerPieces.Fill()
	require.Empty(t, picker.PickAndClaim(nil, PickRequest{
		Bitfield:      peerPieces,
		BlockedPieces: bm.NewLockFreeBitmap(d.info.NumPieces),
		PeerID:        2,
		NumBlocks:     1,
	}), "queued downloads must not create new claims")

	d.resChan = make(chan chunkSubmit, 1)
	go d.backgroundResHandler()
	d.resChan <- chunkSubmit{
		res: &proto.ChunkResponse{
			PieceIndex: 0,
			Begin:      0,
			Data:       make([]byte, defaultBlockSize),
		},
		peerID: 1,
		claim:  claim,
	}

	require.Eventually(t, func() bool {
		stats := picker.DebugStats()
		return stats.ActiveClaims == 0 && stats.RespondedBlocks == 1
	}, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, PendingDownloading, d.GetState())
	require.True(t, validTransition(PendingDownloading, Seeding))
}

func TestResumeEnablesClaimsAfterStop(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	_, err := d.transition(Stopped)
	require.NoError(t, err)
	_, err = d.transition(Downloading)
	require.NoError(t, err)

	claim := claimBlockForTest(t, d, 0, 0, 1)
	require.NotEqual(t, BlockClaim{}, claim)
	require.Equal(t, 1, d.picker.Load().DebugStats().ActiveClaims)
}
