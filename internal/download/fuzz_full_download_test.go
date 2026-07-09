// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/proto"
)

// FuzzFullDownload simulates a realistic download with multiple peers,
// random bitfields, and random disconnects.
func FuzzFullDownload(f *testing.F) {
	f.Add(uint32(5), uint32(4), int64(123))
	f.Add(uint32(10), uint32(4), int64(456))
	f.Add(uint32(8), uint32(8), int64(789))
	f.Add(uint32(20), uint32(2), int64(101))
	f.Add(uint32(3), uint32(6), int64(202))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed int64) {
		numPieces = max(2, min(numPieces, 20))
		blocksPerPiece = max(2, min(blocksPerPiece, 10))
		rng := &seededRand{seed: uint64(seed)}

		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
		d.resChan = make(chan *proto.ChunkResponse, 100)
		d.state.Store(uint32(Downloading))
		go d.backgroundResHandler()

		ctx, cancel := context.WithCancel(d.ctx)
		defer cancel()

		schedulerDone := make(chan struct{})
		go func() {
			defer close(schedulerDone)
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				anyAlive := false
				d.peers.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() {
						anyAlive = true
						d.requestABlock(p)
					}
					return true
				})
				if !anyAlive {
					return
				}
			}
		}()

		numPeers := int(rng.next()%6) + 1
		type pd struct {
			p          *mockPeer
			disconnect time.Duration
		}
		peers := make([]pd, 0, numPeers)

		for i := range numPeers {
			p := newMockPeer()
			p.resChan = d.resChan
			p.info = d.info
			p.dl = d
			p.peerID = uint64(i + 1)
			p.setNumPieces(numPieces)

			for pi := range numPieces {
				if rng.next()%10 < 7 {
					p.bitmap.Set(pi)
				}
			}
			if p.bitmap.Count() == 0 {
				p.bitmap.Set(uint32(rng.next() % uint64(numPieces)))
			}
			p.setDesiredSize(int(rng.next()%8) + 2)

			d.peers.Store(p.ID(), p)
			for pi := range numPieces {
				if p.bitmap.Contains(pi) {
					d.picker.Load().incRefcount(pi)
				}
			}

			var dc time.Duration
			if rng.next()%3 == 0 {
				dc = time.Duration(rng.next()%500+100) * time.Millisecond
			}
			peers = append(peers, pd{p: p, disconnect: dc})
		}

		// Ensure every piece has at least one peer.
		for pi := range numPieces {
			covered := false
			for _, pp := range peers {
				if pp.p.bitmap.Contains(pi) {
					covered = true
					break
				}
			}
			if !covered {
				idx := int(rng.next() % uint64(len(peers)))
				peers[idx].p.bitmap.Set(pi)
				d.picker.Load().incRefcount(pi)
			}
		}

		// Schedule disconnects.
		for _, pi := range peers {
			if pi.disconnect == 0 {
				continue
			}
			go func(p *mockPeer, d time.Duration) {
				timer := time.NewTimer(d)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					p.Close()
				}
			}(pi.p, pi.disconnect)
		}

		// Check for uncovered pieces: if any missing piece has no available peer,
		// the download is dead (not a code bug, just peers all disconnected or
		// the piece was only on a disconnected peer).
		hasCoverageLoss := func() bool {
			for pi := range numPieces {
				if !d.completedBm.Contains(pi) && !d.picker.Load().pieceIsAvailable(pi) {
					return true
				}
			}
			return false
		}

		timer := time.NewTimer(10 * time.Second)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		defer timer.Stop()

		lastCount := uint32(0)
		stallStart := time.Time{}

		for {
			select {
			case <-timer.C:
				count := d.completedBm.Count()
				dump := d.picker.Load().debugDump(d.info)
				var missing []uint32
				for pi := range numPieces {
					if !d.completedBm.Contains(pi) {
						missing = append(missing, pi)
					}
				}
				if hasCoverageLoss() {
					t.Logf("seed=%d uncovered after deadline: %d/%d missing=%v (peers lost coverage)",
						seed, count, numPieces, missing)
					return
				}
				t.Errorf("seed=%d deadline: %d/%d missing=%v\nDUMP:\n%s",
					seed, count, numPieces, missing, dump)
				return
			case <-tick.C:
				count := d.completedBm.Count()
				if count == numPieces {
					return
				}
				if hasCoverageLoss() {
					var missing []uint32
					for pi := range numPieces {
						if !d.completedBm.Contains(pi) {
							missing = append(missing, pi)
						}
					}
					t.Logf("seed=%d uncovered: %d/%d missing=%v (peers lost coverage, not a stall)",
						seed, count, numPieces, missing)
					return
				}

				if count == lastCount {
					if stallStart.IsZero() {
						stallStart = time.Now()
					} else if time.Since(stallStart) > 6*time.Second {
						dump := d.picker.Load().debugDump(d.info)
						var missing []uint32
						for pi := range numPieces {
							if !d.completedBm.Contains(pi) {
								missing = append(missing, pi)
							}
						}
						t.Errorf("seed=%d stalled: %d/%d missing=%v heap=%d\nDUMP:\n%s",
							seed, count, numPieces, missing, d.chunk.heap.Len(), dump)
						return
					}
				} else {
					stallStart = time.Time{}
					lastCount = count
				}
			}
		}
	})
}

var _ = context.Background
