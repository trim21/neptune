// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"math/rand/v2"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// asyncPeer creates a mockPeer configured for async response delivery.
func asyncPeer(d *Download, numPieces uint32, seed uint64) *mockPeer {
	rng := rand.New(rand.NewPCG(seed, seed>>32))

	p := newMockPeer()
	p.resChan = d.resChan
	p.info = d.info
	p.dl = d
	p.peerID = rng.Uint64()
	p.setNumPieces(numPieces)

	for pi := range numPieces {
		if rng.IntN(3) > 0 {
			p.bitmap.Set(pi)
		}
	}
	if p.bitmap.Count() == 0 {
		p.bitmap.Set(rng.Uint32N(numPieces))
	}

	p.setDesiredSize(rng.IntN(16) + 1)
	// Don't let the peer choke us — we have no fast pieces, so a choked
	// peer never receives any blocks and the download stalls.
	_ = rng.IntN(4) // consume entropy for seed compatibility
	return p
}

// FuzzDownloadMultiPeer_Async fuzzes the download with async mock peers
// that push responses through resChan to the real backgroundResHandler.
func FuzzDownloadMultiPeer_Async(f *testing.F) {
	f.Add(uint8(4), uint8(4), uint8(3), uint64(42))

	f.Fuzz(func(t *testing.T,
		numPieces8 uint8,
		blocksPerPiece8 uint8,
		numPeers8 uint8,
		seed uint64,
	) {
		numPieces := uint32(max(numPieces8%5, 2))
		blocksPerPiece := uint32(max(blocksPerPiece8%4, 2))
		numPeers := max(int(numPeers8%4), 1)

		rng := rand.New(rand.NewPCG(seed, seed>>32))

		var corruptPieces []uint32
		for pi := range numPieces {
			if rng.IntN(3) == 0 {
				corruptPieces = append(corruptPieces, pi)
			}
		}

		d := newTestDownload(t, numPieces, blocksPerPiece,
			func(info meta.Info) piece_store.PieceStore {
				mem := piece_store.NewMemStore(info)
				if len(corruptPieces) > 0 {
					return NewFailNPieceStore(mem, corruptPieces)
				}
				return mem
			})
		d.state.Store(uint32(Downloading))
		d.resChan = make(chan *proto.ChunkResponse, 100)
		go d.backgroundResHandler()

		// Create mock peers with async delivery.
		combined := bm.New(numPieces)
		for i := range numPeers {
			p := asyncPeer(d, numPieces, seed+uint64(i))
			d.peers.Store(p.ID(), p)
			t.Cleanup(p.Close)
			p.bitmap.Range(func(pi uint32) {
				d.picker.Load().incRefcount(pi)
				combined.Set(pi)
			})
		}

		// Ensure every piece has at least one peer, or skip.
		if combined.Count() != numPieces {
			return
		}

		// Request loop: periodically call requestABlock for all peers.
		deadline := time.After(2 * time.Second)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()

		lastCompleted := uint32(0)
		stallTicks := 0

		for {
			select {
			case <-deadline:
				hasCapable := false
				d.peers.Range(func(_ uint64, p Peer) bool {
					if !p.Closed() && !p.IsChoking() {
						hasCapable = true
						return false
					}
					return true
				})
				if hasCapable && d.completedBm.Count() < numPieces {
					t.Errorf("download incomplete: %d/%d, corrupt=%v, seed=%d",
						d.completedBm.Count(), numPieces, corruptPieces, seed)
				}
				return

			case <-ticker.C:
				if d.completedBm.Count() == numPieces {
					return
				}

				// Call requestABlock for all open peers.
				d.peers.Range(func(_ uint64, p Peer) bool {
					if p.Closed() {
						return true
					}
					d.requestABlock(p)
					return true
				})

				// Random events.
				if rng.IntN(20) == 0 {
					d.peers.Range(func(_ uint64, p Peer) bool {
						if rng.IntN(3) == 0 {
							if p.IsOurChoking() {
								p.SendUnchoke()
							} else {
								p.SendChoke()
							}
						}
						return true
					})
				}
				if rng.IntN(30) == 0 {
					var toClose []Peer
					d.peers.Range(func(_ uint64, p Peer) bool {
						if rng.IntN(3) == 0 {
							toClose = append(toClose, p)
						}
						return true
					})
					for _, p := range toClose {
						p.Close()
					}
				}

				if d.completedBm.Count() != lastCompleted {
					lastCompleted = d.completedBm.Count()
					stallTicks = 0
				} else {
					stallTicks++
					// After 500ms of no progress, give the async handlers a breather.
					if stallTicks > 500 {
						time.Sleep(50 * time.Millisecond)
						stallTicks = 0
					}
				}
			}
		}
	})
}
