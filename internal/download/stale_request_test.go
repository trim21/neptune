// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// FuzzStaleRequest verifies that no block remains in "requested" state
// after all peers are closed. A background goroutine aggressively closes
// peers while requestABlock runs, exploiting the 1µs delay between
// markAsRequesting and EnqueueBlock.
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
		d.resChan = make(chan *proto.ChunkResponse, 100)
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

		stop := make(chan struct{})
		defer close(stop)

		// Concurrent closer: starts 500µs after request loop, fires every
		// 500µs — guaranteed to hit the 1ms window between markAsRequesting
		// and EnqueueBlock for at least the first requestABlock call.
		go func() {
			time.Sleep(500 * time.Microsecond)
			ticker := time.NewTicker(500 * time.Microsecond)
			defer ticker.Stop()
			for range ticker.C {
				select {
				case <-stop:
					return
				default:
				}
				d.peers.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() {
						p.Close()
					}
					return true
				})
			}
		}()

		// Request loop: runs faster than closer to ensure markAsRequesting
		// fires before Close() has a chance.
		deadline := time.After(3 * time.Second)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()

	loop:
		for {
			select {
			case <-deadline:
				break loop
			case <-ticker.C:
				if d.completedBm.Count() == numPieces {
					break loop
				}
				d.peers.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() {
						p.(*mockPeer).requestABlock()
					}
					return true
				})
			}
		}

		d.peers.Range(func(_ uint64, p Peer) bool {
			p.Close()
			return true
		})
		time.Sleep(50 * time.Millisecond)

		pp := d.picker.Load()
		if pp == nil {
			return
		}
		qs := pp.DebugStats().DownloadQueue
		if qs != 0 {
			t.Errorf("stale request: downloadQueueSize=%d after all peers closed, seed=%d",
				qs, seed)
		}
	})
}
