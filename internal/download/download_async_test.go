// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"fmt"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
	"neptune/internal/proto"
)

// asyncHelper starts the background goroutines and returns a stop function.
func asyncHelper(d *Download) func() {
	d.resChan = make(chan *proto.ChunkResponse, 100)
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
				d.peers.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() {
						d.requestABlock(p)
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
	p.peerID = seed
	p.setNumPieces(numPieces)
	p.bitmap.Fill()
	p.setDesiredSize(4)
	return p
}

// waitDownload polls until all pieces complete or deadline.
func waitDownload(t *testing.T, d *Download, numPieces uint32, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			return false
		case <-ticker.C:
			if d.completedBm.Count() == numPieces {
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
				d.peers.Store(p.ID(), p)
				for pi := range numPieces {
					d.picker.Load().incRefcount(pi)
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
		numPeers   int
	}{
		{"half fail", []uint32{0, 2, 4, 6}, 1},
		{"all fail", []uint32{0, 1, 2, 3, 4, 5, 6, 7}, 1},
		{"one fail", []uint32{3}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDownload(t, numPieces, blocksPerPiece,
				func(info meta.Info) piece_store.PieceStore {
					return NewFailNPieceStore(
						piece_store.NewMemStore(info), tc.failPieces)
				})
			cancel := asyncHelper(d)
			defer cancel()

			for i := range tc.numPeers {
				p := fullPeer(d, numPieces, uint64(i+1))
				d.peers.Store(p.ID(), p)
				for pi := range numPieces {
					d.picker.Load().incRefcount(pi)
				}
			}

			if !waitDownload(t, d, numPieces, 5*time.Second) {
				t.Fatalf("%s: only %d/%d completed", tc.name,
					d.completedBm.Count(), numPieces)
			}
		})
	}
}
