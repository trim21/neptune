// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"testing"
	"time"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// FuzzFullDownload drives the real peer and picker through BitTorrent wire
// messages. The remote peers only know about their net.Pipe endpoint: the test
// never asks a local peer to pick or submit a block directly.
func FuzzFullDownload(f *testing.F) {
	f.Add(uint32(5), uint32(4), int64(123))
	f.Add(uint32(10), uint32(4), int64(456))
	f.Add(uint32(8), uint32(8), int64(789))
	f.Add(uint32(20), uint32(2), int64(101))
	f.Add(uint32(3), uint32(6), int64(202))

	f.Fuzz(func(t *testing.T, numPieces uint32, blocksPerPiece uint32, seed int64) {
		numPieces = max(2, min(numPieces, 20))
		blocksPerPiece = max(2, min(blocksPerPiece, 10))
		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)>>32))

		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
		d.resChan = make(chan chunkSubmit, 100)
		go d.backgroundResHandler()

		done := make(chan struct{})
		defer close(done)
		remoteErrors := make(chan error, 8)

		// Keep one complete, reliable seed so random disconnects and rejects do
		// not turn a scheduler failure into an expected coverage loss.
		numPeers := int(rng.Uint64()%5) + 2
		for i := range numPeers {
			pieces := bm.New(numPieces)
			if i == 0 {
				pieces.Fill()
			} else {
				for pieceIndex := range numPieces {
					if rng.Uint64()%10 < 7 {
						pieces.Set(pieceIndex)
					}
				}
				if pieces.Count() == 0 {
					pieces.Set(uint32(rng.Uint64() % uint64(numPieces)))
				}
			}

			var peerID proto.PeerID
			copy(peerID[:], fmt.Sprintf("-NF%05d-%011d", i, uint64(seed)))
			peerRNG := rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))
			addr := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(30000+i))
			remote, _ := pipePeer(d, addr, pieces, peerID, peerRNG)

			cfg := remoteConfig{maxLatency: time.Duration(rng.Uint64()%4) * time.Millisecond}
			if i != 0 {
				if rng.Uint64()%2 == 0 {
					cfg.disconnectAfterRequests = uint32(rng.Uint64()%12) + 1
				}
				if rng.Uint64()%3 == 0 {
					cfg.rejectEvery = uint32(rng.Uint64()%5) + 2
				}
				if rng.Uint64()%3 == 0 {
					cfg.chokeAfter = time.Duration(rng.Uint64()%20+1) * time.Millisecond
					cfg.unchokeAfter = time.Duration(rng.Uint64()%20+1) * time.Millisecond
				}
			}

			go func() {
				if err := remote.Run(cfg, done, d.info.PieceLength); !expectedRemoteClose(err) {
					select {
					case remoteErrors <- err:
					case <-done:
					}
				}
			}()
		}

		deadline := time.NewTimer(10 * time.Second)
		tick := time.NewTicker(100 * time.Millisecond)
		defer deadline.Stop()
		defer tick.Stop()

		lastCount := uint32(0)
		stallStart := time.Now()
		for {
			select {
			case err := <-remoteErrors:
				t.Fatalf("seed=%d: remote peer failed: %v", seed, err)
			case <-deadline.C:
				t.Fatalf("seed=%d: deadline: %s", seed, wireDownloadState(d, numPieces))
			case <-tick.C:
				count := d.completedBm.Count()
				if count == numPieces {
					return
				}
				if count != lastCount {
					lastCount = count
					stallStart = time.Now()
					continue
				}
				if time.Since(stallStart) > 6*time.Second {
					t.Fatalf("seed=%d: stalled: %s", seed, wireDownloadState(d, numPieces))
				}
			}
		}
	})
}

func expectedRemoteClose(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.ErrClosedPipe)
}

func wireDownloadState(d *Download, numPieces uint32) string {
	missing := make([]uint32, 0, numPieces-d.completedBm.Count())
	for pieceIndex := range numPieces {
		if !d.completedBm.Contains(pieceIndex) {
			missing = append(missing, pieceIndex)
		}
	}

	picker := d.picker.Load()
	if picker == nil {
		return fmt.Sprintf("state=%s complete=%d/%d missing=%v peers=%d picker=released",
			d.GetState(), d.completedBm.Count(), numPieces, missing, d.peers.Size())
	}
	return fmt.Sprintf("state=%s complete=%d/%d missing=%v peers=%d\n%s",
		d.GetState(), d.completedBm.Count(), numPieces, missing, d.peers.Size(), picker.DebugDump())
}
