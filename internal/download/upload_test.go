// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/empty"
	"neptune/internal/proto"
)

func TestBackgroundReqHandlerDoesNotQueueClaimedRequestAgain(t *testing.T) {
	d := newTestDownload(t, 1, 1, piece_store.NewMemStore)
	d.session.UploadQ = make(chan func(), 8)

	p := newMockPeer()
	p.dl = d
	d.peerList.activeByID.Store(p.ID(), p)

	req := proto.ChunkRequest{PieceIndex: 0, Begin: 0, Length: defaultBlockSize}
	require.True(t, p.AddPeerRequest(req))

	done := make(chan struct{})
	go func() {
		d.backgroundReqHandler()
		close(done)
	}()

	d.scheduleResponseSignal <- empty.Empty{}
	require.Eventually(t, func() bool {
		return len(d.session.UploadQ) == 1
	}, time.Second, time.Millisecond)

	// The first task remains queued and owns the request. A later wake must not
	// enqueue the same request again while that ownership is outstanding.
	d.scheduleResponseSignal <- empty.Empty{}
	require.Eventually(t, func() bool {
		return len(d.scheduleResponseSignal) == 0
	}, time.Second, time.Millisecond)
	require.Len(t, d.session.UploadQ, 1)

	d.cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("backgroundReqHandler did not exit")
	}
}
