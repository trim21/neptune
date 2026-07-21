// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
)

// FuzzStaleRequest verifies that no block remains in "requested" state
// after all peers are closed. A background goroutine aggressively closes
// peers while requestABlock transfers picker ownership into peer queues.
func FuzzStaleRequest(f *testing.F) {
	f.Add(uint8(4), uint8(4), uint8(3), uint64(42))

	f.Fuzz(func(t *testing.T,
		numPieces8 uint8,
		blocksPerPiece8 uint8,
		numPeers8 uint8,
		seed uint64,
	) {
		numPieces := uint32(max(numPieces8%5, 2))
		blocksPerPiece := uint32(max(blocksPerPiece8%5, 2))
		numPeers := max(int(numPeers8%5), 2)

		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
		d.state.Store(uint32(Downloading))
		d.resChan = make(chan chunkSubmit, 100)
		go d.backgroundResHandler()

		combined := bm.New(numPieces)
		for i := range numPeers {
			p := asyncPeer(d, numPieces, seed+uint64(i))
			d.peers.Store(p.ID(), p)
			p.bitmap.Range(func(pi uint32) {
				d.picker.Load().IncRefcount(pi)
				combined.Set(pi)
			})
		}
		if combined.Count() != numPieces {
			return
		}

		// Concurrent closer races with picker-to-peer ownership transfers. One
		// pass is sufficient because this test does not add peers afterward.
		closerDone := make(chan struct{})
		go func() {
			defer close(closerDone)
			time.Sleep(500 * time.Microsecond)
			d.peers.Range(func(_ uint64, p Peer) bool {
				p.Close()
				return true
			})
		}()

		// Keep scheduling until Close has synchronously consumed every claim it
		// can see. A racing requestABlock call completes before this loop exits
		// and must consume or release every claim it picked.
	loop:
		for {
			select {
			case <-closerDone:
				break loop
			default:
			}

			d.peers.Range(func(_ uint64, p Peer) bool {
				if !p.Closed() {
					p.(*mockPeer).requestABlock()
				}
				return true
			})
		}

		// Close is idempotent and synchronous. This final pass covers peers that
		// were skipped by a racing map traversal. Calling requestABlock afterward
		// waits on reqMu for any scheduler call that passed its initial Closed
		// check before Close; once it returns, no such call can retain a claim.
		d.peers.Range(func(_ uint64, p Peer) bool {
			p.Close()
			p.(*mockPeer).requestABlock()
			return true
		})

		pp := d.picker.Load()
		if pp == nil {
			return
		}
		stats := pp.DebugStats()
		if stats.DownloadQueue != 0 || stats.RequestedBlocks != 0 || stats.ActiveClaims != 0 {
			t.Errorf("stale request after all peers closed: queue=%d requested=%d claims=%d seed=%d",
				stats.DownloadQueue, stats.RequestedBlocks, stats.ActiveClaims, seed)
		}
	})
}
