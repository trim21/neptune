// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
)

// asyncHelper starts the background goroutines and returns a stop function.
func asyncHelper(d *Download) func() {
	d.resChan = make(chan chunkSubmit, 100)
	d.state.Store(uint32(Downloading))
	go d.backgroundResHandler()

	ctx, cancel := context.WithCancel(d.ctx)
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.peerList.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() {
						p.(*mockPeer).requestABlock()
					}
					return true
				})
			}
		}
	}()
	return cancel
}

// fullPeer creates a mock peer whose bitmap covers all pieces.
func fullPeer(d *Download, numPieces uint32, seed uint64) *mockPeer {
	p := newMockPeer()
	p.resChan = d.resChan
	p.info = d.info
	p.dl = d
	p.peerID = seed
	p.setNumPieces(numPieces)
	p.bitmap.Fill()
	p.setDesiredSize(4)
	return p
}

// downloadStallDump summarizes the state behind a wait that did not finish:
// which pieces are missing, the picker's block/claim accounting, and each
// peer's queue.
func downloadStallDump(d *Download, numPieces uint32) string {
	var missing []uint32
	for pi := range numPieces {
		if !d.completedBm.Contains(pi) {
			missing = append(missing, pi)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "completed=%d/%d missing=%v", d.completedBm.Count(), numPieces, missing)

	st := d.picker.Load().DebugStats()
	fmt.Fprintf(&b,
		" picker{requested=%d responded=%d free=%d activeClaims=%d dupClaims=%d queue=%d downloading=%d open=%d staleAccepts=%d staleReleases=%d}",
		st.RequestedBlocks, st.RespondedBlocks, st.FreeBlocks, st.ActiveClaims, st.DuplicateClaims,
		st.DownloadQueue, st.Downloading, st.OpenPieces, st.StaleAccepts, st.StaleReleases)

	d.peerList.Range(func(_ uint64, p Peer) bool {
		fmt.Fprintf(&b, " peer{%d closed=%v blocked=%d", p.ID(), p.Closed(), p.BlockedCount())
		if mp, ok := p.(*mockPeer); ok {
			fmt.Fprintf(&b, " queued=%d outstanding=%d", mp.QueueLen(), mp.OutstandingRequests())
		}
		b.WriteString("}")
		return true
	})
	return b.String()
}

// waitDownload polls until all pieces complete. A download that keeps making
// progress gets the whole timeout; one that reports no newly completed piece
// for the stall window fails immediately with its state, so a piece that can
// no longer be claimed is reported instead of being masked by a slow runner.
func waitDownload(t *testing.T, d *Download, numPieces uint32, timeout time.Duration) bool {
	t.Helper()
	stallTimeout := min(5*time.Second, timeout)
	deadline := time.Now().Add(timeout)
	lastCount := d.completedBm.Count()
	lastProgress := time.Now()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		count := d.completedBm.Count()
		if count >= numPieces {
			return true
		}
		if count != lastCount {
			lastCount = count
			lastProgress = time.Now()
			continue
		}
		if time.Since(lastProgress) >= stallTimeout {
			t.Logf("download stalled at %d/%d pieces: %s", count, numPieces, downloadStallDump(d, numPieces))
			return false
		}
		if time.Now().After(deadline) {
			t.Logf("download timed out at %d/%d pieces: %s", count, numPieces, downloadStallDump(d, numPieces))
			return false
		}
	}
	return false
}

func waitForFailedPieces(t *testing.T, store *FailNPieceStore, count int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			store.mu.Lock()
			failed := len(store.failed)
			store.mu.Unlock()
			if failed == count {
				return true
			}
		}
	}
}

// ── Scenario 1: full peers, no corruption ────────────────────────────

func TestAsyncDownload_FullPeer(t *testing.T) {
	const numPieces uint32 = 8
	const blocksPerPiece uint32 = 4

	for _, numPeers := range []int{1, 2} {
		t.Run(fmt.Sprintf("peers=%d", numPeers), func(t *testing.T) {
			d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
			cancel := asyncHelper(d)
			defer cancel()

			for i := range numPeers {
				p := fullPeer(d, numPieces, uint64(i+1))
				d.peerList.activeByID.Store(p.ID(), p)
				for pi := range numPieces {
					d.picker.Load().IncRefcount(pi)
				}
			}

			if !waitDownload(t, d, numPieces, 2*time.Second) {
				t.Fatalf("%d peers: only %d/%d completed", numPeers,
					d.completedBm.Count(), numPieces)
			}
		})
	}
}

// ── Scenario 2: corrupt piece recovery ───────────────────────────────

func TestAsyncDownload_CorruptRecovery(t *testing.T) {
	const numPieces uint32 = 8
	const blocksPerPiece uint32 = 4

	for _, tc := range []struct {
		name       string
		failPieces []uint32
	}{
		{"half fail", []uint32{0, 2, 4, 6}},
		{"all fail", []uint32{0, 1, 2, 3}},
		{"one fail", []uint32{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var failStore *FailNPieceStore
			d := newTestDownload(t, numPieces, blocksPerPiece,
				func(info meta.Info) piece_store.PieceStore {
					failStore = NewFailNPieceStore(
						piece_store.NewMemStore(info), tc.failPieces)
					return failStore
				})
			cancel := asyncHelper(d)
			defer cancel()

			// Peer 1: first download attempt. Will get blocked for pieces
			// that fail hash due to FailNPieceStore.
			p1 := fullPeer(d, numPieces, 1)
			d.peerList.activeByID.Store(p1.ID(), p1)
			for pi := range numPieces {
				d.picker.Load().IncRefcount(pi)
			}

			// Wait until the first peer has exercised every configured failure,
			// then add a fresh peer that did not contribute to those attempts.
			// The old fixed 500ms settling delay was scheduler-dependent under
			// race/coverage instrumentation and did not express this invariant.
			if !waitForFailedPieces(t, failStore, len(tc.failPieces), 5*time.Second) {
				t.Fatalf("%s: timed out waiting for first hash-check pass", tc.name)
			}

			p2 := fullPeer(d, numPieces, 2)
			d.peerList.activeByID.Store(p2.ID(), p2)
			for pi := range numPieces {
				d.picker.Load().IncRefcount(pi)
			}

			if !waitDownload(t, d, numPieces, 30*time.Second) {
				t.Fatalf("%s: recovery did not finish, only %d/%d completed", tc.name,
					d.completedBm.Count(), numPieces)
			}
		})
	}
}

// ── Scenario 3: parole mode bans a peer after 4 hash-failed pieces ───

func TestAsyncDownload_ParoleBan(t *testing.T) {
	const numPieces uint32 = 8
	const blocksPerPiece uint32 = 4

	failPieces := []uint32{0, 1, 2, 3, 4, 5, 6, 7}

	d := newTestDownload(t, numPieces, blocksPerPiece,
		func(info meta.Info) piece_store.PieceStore {
			return NewFailNPieceStore(piece_store.NewMemStore(info), failPieces)
		})
	cancel := asyncHelper(d)
	defer cancel()

	p1 := fullPeer(d, numPieces, 1)
	d.peerList.activeByID.Store(p1.ID(), p1)
	for pi := range numPieces {
		d.picker.Load().IncRefcount(pi)
	}

	// After 4 failed pieces, trust_points reaches -7 and the peer is closed.
	waitForClosed(t, p1, 5*time.Second)

	if !p1.Closed() {
		t.Error("peer should have been banned after 4+ hash-failed pieces")
	}
}

func waitForClosed(t *testing.T, p *mockPeer, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if p.Closed() {
			return
		}
		select {
		case <-deadline:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
