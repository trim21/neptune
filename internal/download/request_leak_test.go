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
	require.True(t, picker.TryMarkAsRequesting(0, 0, false))
	require.Equal(t, 1, picker.DebugStats().DownloadQueue)

	// Download leaves Downloading before the in-flight response is handled.
	d.state.Store(uint32(Stopped))

	d.resChan = make(chan chunkSubmit, 1)
	go d.backgroundResHandler()

	d.resChan <- chunkSubmit{
		res: &proto.ChunkResponse{
			PieceIndex: 0, Begin: 0,
			Data: make([]byte, defaultBlockSize),
		},
		peerID: 1,
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
		myRequests: xsync.NewMap[proto.ChunkRequest, time.Time](),
	}

	// One block in flight (on the wire, tracked in myRequests).
	require.True(t, picker.TryMarkAsRequesting(0, 0, false))
	p.myRequests.Store(pieceChunk(d.info, 0, 0), time.Now())

	// One block queued locally (marked Requested, not yet sent).
	require.True(t, picker.TryMarkAsRequesting(0, 1, false))
	require.True(t, p.requestQueue.Push(PieceBlock{PieceIndex: 0, BlockIndex: 1}))

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
