// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/client/tracker"
	"neptune/internal/piece_store"
)

// testTurnoverDownload builds a Download with a peer list and a per-torrent
// connection limit for turnover tests. The limit must be >= 6 because
// peerTurnover deliberately skips small torrents (maxConn < 6).
func testTurnoverDownload(t *testing.T, connLimit int) *Download {
	t.Helper()
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.peerList = newPeerList(d)
	d.session.TorrentConnLimit.Store(uint32(connLimit))
	return d
}

// addConnectedPeer adds a mock peer past the 30s grace period with interest
// in the peer's pieces (so it gets a normal score), and returns it.
func addConnectedPeer(d *Download, id uint64, p *mockPeer) *mockPeer {
	p.connectedAt = time.Now().Add(-time.Hour)
	p.SetOurInterested(true)
	d.peerList.activeByID.Store(id, p)
	return p
}

func addCandidate(d *Download) {
	d.peerList.addPeer(netip.MustParseAddrPort("10.0.0.1:6881"), tracker.PeerSourceTracker)
}

func countClosed(peers ...*mockPeer) int {
	n := 0
	for _, p := range peers {
		if p.Closed() {
			n++
		}
	}
	return n
}

// TestPeerTurnoverBelowLimitNoEviction verifies that turnover does not
// evict any peer while the connection count is below the per-torrent limit:
// new candidates can connect directly without disrupting existing peers.
func TestPeerTurnoverBelowLimitNoEviction(t *testing.T) {
	d := testTurnoverDownload(t, 10)

	var peers = make([]*mockPeer, 0, 8)
	for i := range uint64(8) {
		peers = append(peers, addConnectedPeer(d, i+1, newMockPeer()))
	}
	// A candidate is waiting, but the limit (6) is not reached yet.
	addCandidate(d)

	d.peerTurnover()

	for _, p := range peers {
		require.False(t, p.Closed(), "peer must not be evicted below the connection limit")
	}
}

// TestPeerTurnoverAtLimitEvictsLowest verifies that at the connection limit
// with candidates waiting, the lowest-scored peer is evicted to free a slot.
func TestPeerTurnoverAtLimitEvictsLowest(t *testing.T) {
	d := testTurnoverDownload(t, 10)
	addCandidate(d)

	good1 := addConnectedPeer(d, 1, newMockPeer())
	good2 := addConnectedPeer(d, 2, newMockPeer())
	good3 := addConnectedPeer(d, 3, newMockPeer())
	good4 := addConnectedPeer(d, 4, newMockPeer())
	good5 := addConnectedPeer(d, 5, newMockPeer())
	good6 := addConnectedPeer(d, 6, newMockPeer())
	good7 := addConnectedPeer(d, 7, newMockPeer())
	good8 := addConnectedPeer(d, 8, newMockPeer())
	good9 := addConnectedPeer(d, 9, newMockPeer())
	bad := addConnectedPeer(d, 10, newMockPeer())
	bad.setChoking(true) // choking with no transfer history → score 2

	d.peerTurnover()

	require.True(t, bad.Closed(), "lowest-scored peer must be evicted at the limit")
	require.False(t, good1.Closed())
	require.False(t, good2.Closed())
	require.False(t, good3.Closed())
	require.False(t, good4.Closed())
	require.False(t, good5.Closed())
	require.False(t, good6.Closed())
	require.False(t, good7.Closed())
	require.False(t, good8.Closed())
	require.False(t, good9.Closed())
}

// TestPeerTurnoverKeepsFastPeer verifies that a peer transferring at or
// above fastPeerRateThreshold is exempt from eviction even at the limit,
// while a slow peer is evicted instead.
func TestPeerTurnoverKeepsFastPeer(t *testing.T) {
	d := testTurnoverDownload(t, 10)
	addCandidate(d)

	fast := addConnectedPeer(d, 1, newMockPeer())
	fast.SetRate(2 * 1024 * 1024) // 2 MiB/s download → exempt

	var slow = make([]*mockPeer, 0, 9)
	for i := range uint64(9) {
		slow = append(slow, addConnectedPeer(d, i+2, newMockPeer()))
	}

	d.peerTurnover()

	require.False(t, fast.Closed(), "fast peer must never be turned over")
	require.Equal(t, 1, countClosed(slow...), "a slow peer must be evicted instead of the fast one")
}

// TestPeerTurnoverAllFastNoEviction verifies that when every connected peer
// is fast, nothing is evicted at all.
func TestPeerTurnoverAllFastNoEviction(t *testing.T) {
	d := testTurnoverDownload(t, 10)
	addCandidate(d)

	var peers = make([]*mockPeer, 0, 10)
	for i := range uint64(10) {
		p := addConnectedPeer(d, i+1, newMockPeer())
		p.SetRate(2 * 1024 * 1024)
		peers = append(peers, p)
	}

	d.peerTurnover()

	require.Equal(t, 0, countClosed(peers...), "all-fast peer pool must not be evicted")
}

// TestEvictPeersLimitedByCandidates verifies that EvictPeers never evicts
// more peers than there are connect candidates, and returns the count.
func TestEvictPeersLimitedByCandidates(t *testing.T) {
	d := testTurnoverDownload(t, 10)

	// Only 2 candidates available.
	addCandidate(d)
	d.peerList.addPeer(netip.MustParseAddrPort("10.0.0.2:6881"), tracker.PeerSourceTracker)

	var peers = make([]*mockPeer, 0, 4)
	for i := range uint64(4) {
		peers = append(peers, addConnectedPeer(d, i+1, newMockPeer()))
	}

	// Request 4 evictions, but only 2 candidates exist.
	evicted := d.EvictPeers(4)

	require.Equal(t, 2, evicted, "eviction is capped by the number of candidates")
	require.Equal(t, 2, countClosed(peers...))
}

// TestEvictPeersNoCandidatesNoEviction verifies that with zero candidates
// waiting, no peer is evicted even when requested.
func TestEvictPeersNoCandidatesNoEviction(t *testing.T) {
	d := testTurnoverDownload(t, 10)

	var peers = make([]*mockPeer, 0, 3)
	for i := range uint64(3) {
		peers = append(peers, addConnectedPeer(d, i+1, newMockPeer()))
	}

	require.Equal(t, 0, d.EvictPeers(2), "no candidates means no eviction")
	require.Equal(t, 0, countClosed(peers...))
}
