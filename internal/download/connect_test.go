// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"neptune/internal/client/tracker"
	"neptune/internal/config"
	"neptune/internal/piece_store"
	"neptune/internal/proto"
	"neptune/internal/session"
)

// ── dispatch logic (no network) ──────────────────────────────────────

// newConnectSession builds a minimal session with the semaphores the
// connection loop needs.
func newConnectSession(t *testing.T, dialSlots int64) *session.Session {
	t.Helper()
	sess := &session.Session{
		ConnSem: semaphore.NewWeighted(200),
		DialSem: semaphore.NewWeighted(dialSlots),
		Config: config.Config{App: config.Application{
			GlobalConnectionLimit: 200,
		}},
	}
	sess.TorrentConnLimit.Store(50)
	sess.ConnCount.Store(0)
	return sess
}

func newConnectDownload(t *testing.T, sess *session.Session) *Download {
	t.Helper()
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.session = sess
	d.connectSignal = make(chan struct{}, 1)
	d.peersCh = make(chan []tracker.DiscoveredPeer, 1)
	return d
}

// TestDispatchConnectionsNonActiveState verifies the early return: downloads
// that are not Downloading/Seeding never dispatch a dial.
func TestDispatchConnectionsNonActiveState(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)
	d.peerList.addPeer(netip.MustParseAddrPort("10.0.0.3:6881"), tracker.PeerSourceTracker)

	for _, state := range []State{Stopped, PendingDownloading, Checking, Moving, Error} {
		d.state.Store(uint32(state))
		d.dispatchConnections()

		d.peerList.mu.Lock()
		idx, found := d.peerList.findPeer(netip.MustParseAddrPort("10.0.0.3:6881"))
		require.True(t, found, "state %s", state)
		require.Zero(t, d.peerList.peers[idx].failcount, "state %s must not dial", state)
		require.False(t, d.peerList.peers[idx].dialing, "state %s must not dial", state)
		d.peerList.mu.Unlock()
	}
}

// TestDispatchConnectionsAtConnectionLimit verifies that a download at its
// per-torrent connection limit stops dispatching: candidates stay untouched.
func TestDispatchConnectionsAtConnectionLimit(t *testing.T) {
	sess := newConnectSession(t, 4)
	sess.TorrentConnLimit.Store(1)
	d := newConnectDownload(t, sess)

	// Occupy the single per-torrent slot with an incoming connection.
	d.peerList.incomingConnectionOpened(netip.MustParseAddrPort("10.0.0.1:6881"))

	addr := netip.MustParseAddrPort("10.0.0.2:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)

	d.dispatchConnections()

	d.peerList.mu.Lock()
	idx, found := d.peerList.findPeer(addr)
	require.True(t, found)
	require.Zero(t, d.peerList.peers[idx].failcount)
	require.False(t, d.peerList.peers[idx].dialing)
	d.peerList.mu.Unlock()
}

// TestDispatchConnectionsNoCandidates verifies the loop stops when the peer
// list has no connectable candidate.
func TestDispatchConnectionsNoCandidates(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)
	require.True(t, sess.DialSem.TryAcquire(4))
	defer sess.DialSem.Release(4)

	done := make(chan struct{})
	go func() {
		d.dispatchConnections()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch blocked on DialSem without a candidate")
	}
}

func TestDispatchSelectsCandidateAfterDialTurn(t *testing.T) {
	sess := newConnectSession(t, 1)
	require.True(t, sess.DialSem.TryAcquire(1))
	blockGlobalConnectionSlot(t, sess)
	d := newConnectDownload(t, sess)
	addr := netip.MustParseAddrPort("10.0.0.14:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)

	done := make(chan struct{})
	go func() {
		d.dispatchConnections()
		close(done)
	}()

	require.Never(t, func() bool {
		return candidateDialing(d, addr)
	}, 100*time.Millisecond, 5*time.Millisecond, "candidate must not be selected before the download acquires DialSem")

	sess.DialSem.Release(1)
	require.Eventually(t, func() bool {
		return candidateDialing(d, addr)
	}, time.Second, 10*time.Millisecond)

	d.cancel()
	sess.ConnSem.Release(1)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not exit after cancellation")
	}
}

func TestDispatchConnectionsSkipsBannedPeer(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)
	addr := netip.MustParseAddrPort("10.0.0.4:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)
	d.banAddr(addr.Addr())

	d.dispatchConnections()

	d.peerList.mu.Lock()
	idx, found := d.peerList.findPeer(addr)
	require.True(t, found)
	require.False(t, d.peerList.peers[idx].dialing)
	d.peerList.mu.Unlock()
	require.True(t, sess.DialSem.TryAcquire(4), "banned peers must not take a dial slot")
	sess.DialSem.Release(4)
}

func TestDispatchConnectionsSkipsConnectedAddress(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)
	addr := netip.MustParseAddrPort("10.0.0.5:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)
	d.peerList.incomingConnectionOpened(addr)

	d.dispatchConnections()

	d.peerList.mu.Lock()
	idx, found := d.peerList.findPeer(addr)
	require.True(t, found)
	require.False(t, d.peerList.peers[idx].dialing)
	d.peerList.mu.Unlock()
	require.True(t, sess.DialSem.TryAcquire(4), "connected peers must not take a dial slot")
	sess.DialSem.Release(4)
}

func TestOutgoingHandshakeFailureReleasesPeerListSlot(t *testing.T) {
	sess := newConnectSession(t, 4)
	sess.TorrentConnLimit.Store(1)
	d := newConnectDownload(t, sess)
	defer d.cancel()

	addr := netip.MustParseAddrPort("10.0.0.11:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)
	require.True(t, sess.ConnSem.TryAcquire(1))
	sess.ConnCount.Add(1)

	local, remote := net.Pipe()
	defer remote.Close()
	p := NewOutgoingPeer(local, d, addr, false)
	require.True(t, d.peerList.newConnection(addr, p, time.Now().Unix()))

	remoteDone := make(chan struct{})
	go func() {
		_, _ = proto.ReadHandshake(remote)
		_ = remote.Close()
		close(remoteDone)
	}()

	require.Eventually(t, func() bool {
		d.peerList.mu.Lock()
		defer d.peerList.mu.Unlock()
		idx, found := d.peerList.findPeer(addr)
		return found && d.peerList.peers[idx].connection == nil
	}, time.Second, 10*time.Millisecond)
	<-remoteDone
	require.Zero(t, d.occupiedConnectionCount())
	require.True(t, sess.ConnSem.TryAcquire(200), "failed handshake must release the global connection slot")
	sess.ConnSem.Release(200)
}

func TestAddConnReservesOutgoingLane(t *testing.T) {
	sess := newConnectSession(t, 4)
	sess.TorrentConnLimit.Store(2)
	d := newConnectDownload(t, sess)
	defer d.cancel()

	firstLocal, firstRemote := net.Pipe()
	defer firstRemote.Close()
	require.True(t, sess.ConnSem.TryAcquire(1))
	sess.ConnCount.Add(1)
	d.AddConn(
		netip.MustParseAddrPort("10.0.0.12:6881"),
		firstLocal,
		proto.Handshake{InfoHash: d.info.Hash},
		false,
	)
	require.Equal(t, 1, d.occupiedConnectionCount())

	secondLocal, secondRemote := net.Pipe()
	defer secondRemote.Close()
	require.True(t, sess.ConnSem.TryAcquire(1))
	sess.ConnCount.Add(1)
	d.AddConn(
		netip.MustParseAddrPort("10.0.0.13:6881"),
		secondLocal,
		proto.Handshake{InfoHash: d.info.Hash},
		false,
	)
	require.Equal(t, 1, d.occupiedConnectionCount())
	_, err := secondRemote.Read(make([]byte, 1))
	require.Error(t, err, "incoming connection must not consume the reserved outgoing lane")

	_, err = proto.ReadHandshake(firstRemote)
	require.NoError(t, err)
	require.NoError(t, firstRemote.Close())
	require.Eventually(t, func() bool {
		return d.occupiedConnectionCount() == 0 && d.peerList.Size() == 0
	}, time.Second, 10*time.Millisecond)
	require.True(t, sess.ConnSem.TryAcquire(200), "all incoming connection slots must be released")
	sess.ConnSem.Release(200)
}

// ── integration (requires network) ───────────────────────────────────

// unreachableAddr returns a local address that refuses connections, so a dial
// fails immediately instead of hanging for the full timeout.
func unreachableAddr(t *testing.T) netip.AddrPort {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	require.NoError(t, ln.Close())
	return addr
}

func candidateFailed(d *Download, addr netip.AddrPort) bool {
	d.peerList.mu.Lock()
	defer d.peerList.mu.Unlock()
	idx, found := d.peerList.findPeer(addr)
	return found && d.peerList.peers[idx].failcount > 0
}

func candidateDialing(d *Download, addr netip.AddrPort) bool {
	d.peerList.mu.Lock()
	defer d.peerList.mu.Unlock()
	idx, found := d.peerList.findPeer(addr)
	return found && d.peerList.peers[idx].dialing
}

func dialingCandidateCount(d *Download) int {
	d.peerList.mu.Lock()
	defer d.peerList.mu.Unlock()
	count := 0
	for _, peer := range d.peerList.peers {
		if peer.dialing {
			count++
		}
	}
	return count
}

func blockGlobalConnectionSlot(t *testing.T, sess *session.Session) {
	t.Helper()
	sess.ConnSem = semaphore.NewWeighted(1)
	sess.Config.App.GlobalConnectionLimit = 1
	require.True(t, sess.ConnSem.TryAcquire(1))
}

func TestDispatchConnectionsSkipsBannedCandidateAndDialsNext(t *testing.T) {
	sess := newConnectSession(t, 4)
	blockGlobalConnectionSlot(t, sess)
	d := newConnectDownload(t, sess)
	d.session.TorrentConnLimit.Store(1)

	banned := netip.MustParseAddrPort("10.0.0.6:6881")
	valid := netip.MustParseAddrPort("10.0.0.7:6881")
	d.peerList.addPeer(valid, tracker.PeerSourcePEX)
	d.peerList.addPeer(banned, tracker.PeerSourceTracker)
	d.banAddr(banned.Addr())

	go d.dispatchConnections()

	require.Eventually(t, func() bool { return candidateDialing(d, valid) }, time.Second, 10*time.Millisecond)
	require.False(t, candidateDialing(d, banned))

	d.cancel()
	require.Eventually(t, func() bool { return dialingCandidateCount(d) == 0 }, time.Second, 10*time.Millisecond)
	sess.ConnSem.Release(1)
}

// TestStopAbandonsWaitingDial verifies that a dial waiting on the global
// connection slot is abandoned once the download stops: it acquires the slot
// when one is released, fails the IsActive re-check, and releases the slot
// without dialing.
func TestStopAbandonsWaitingDial(t *testing.T) {
	sess := newConnectSession(t, 4)
	blockGlobalConnectionSlot(t, sess)
	d := newConnectDownload(t, sess)
	addr := netip.MustParseAddrPort("10.0.0.8:6881")
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)

	go d.dispatchConnections()
	require.Eventually(t, func() bool { return candidateDialing(d, addr) }, time.Second, 10*time.Millisecond)

	require.NoError(t, d.Stop())
	// Stop does not cancel the wait; the next released slot lets the dial
	// observe the inactive state and give up.
	sess.ConnSem.Release(1)
	require.Eventually(t, func() bool { return dialingCandidateCount(d) == 0 }, time.Second, 10*time.Millisecond)
	// DialSem is released by dispatchConnections when tryDial returns. The
	// dialing flag is cleared slightly earlier, so wait for the semaphore
	// itself instead of inferring its release from the candidate state.
	require.Eventually(t, func() bool { return sess.DialSem.TryAcquire(4) }, time.Second, 10*time.Millisecond,
		"stopped download must release dial slots")
	sess.DialSem.Release(4)
}

// TestDispatchConnectionsCountsDialingOutgoingSlot verifies the per-torrent
// accounting: while one candidate occupies the only slot, a second dispatch
// pass must not start another dial.
func TestDispatchConnectionsCountsDialingOutgoingSlot(t *testing.T) {
	sess := newConnectSession(t, 4)
	blockGlobalConnectionSlot(t, sess)
	d := newConnectDownload(t, sess)
	d.session.TorrentConnLimit.Store(1)
	d.peerList.addPeer(netip.MustParseAddrPort("10.0.0.9:6881"), tracker.PeerSourceTracker)
	d.peerList.addPeer(netip.MustParseAddrPort("10.0.0.10:6881"), tracker.PeerSourcePEX)

	go d.dispatchConnections()
	require.Eventually(t, func() bool { return dialingCandidateCount(d) == 1 }, time.Second, 10*time.Millisecond)
	d.dispatchConnections()

	require.Equal(t, 1, dialingCandidateCount(d), "second dispatch must not exceed the per-torrent limit")

	d.cancel()
	require.Eventually(t, func() bool { return dialingCandidateCount(d) == 0 }, time.Second, 10*time.Millisecond)
	sess.ConnSem.Release(1)
}

// TestConnectLoopDispatchesDial verifies the full path: a candidate added to
// the peer list plus a wakeup signal makes the connection loop dial it, and
// the dial failure is recorded in the peer's failcount.
func TestConnectLoopDispatchesDial(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)

	go d.connectLoop()

	addr := unreachableAddr(t)
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)
	d.signalConnect()

	require.Eventually(t, func() bool { return candidateFailed(d, addr) }, 5*time.Second, 10*time.Millisecond)
}

// TestConnectLoopFairnessAcrossDownloads verifies that with a single dial
// slot, two downloads both get to dial — the FIFO DialSem cannot starve one
// download.
func TestConnectLoopFairnessAcrossDownloads(t *testing.T) {
	sess := newConnectSession(t, 1) // one dial slot in total
	d1 := newConnectDownload(t, sess)
	d2 := newConnectDownload(t, sess)

	go d1.connectLoop()
	go d2.connectLoop()

	addr1 := unreachableAddr(t)
	addr2 := unreachableAddr(t)
	d1.peerList.addPeer(addr1, tracker.PeerSourceTracker)
	d2.peerList.addPeer(addr2, tracker.PeerSourceTracker)
	d1.signalConnect()
	d2.signalConnect()

	require.Eventually(t, func() bool { return candidateFailed(d1, addr1) }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return candidateFailed(d2, addr2) }, 5*time.Second, 10*time.Millisecond)
}

// TestConnectLoopWaitsForGlobalConnectionSlot verifies that downloads wait on
// ConnSem itself when the global pool is full. Releasing a slot then serves
// both queued downloads without relying on their periodic retry ticks.
func TestConnectLoopWaitsForGlobalConnectionSlot(t *testing.T) {
	sess := newConnectSession(t, 2)
	sess.ConnSem = semaphore.NewWeighted(1)
	sess.Config.App.GlobalConnectionLimit = 1
	require.True(t, sess.ConnSem.TryAcquire(1))
	sess.ConnCount.Store(1)

	d1 := newConnectDownload(t, sess)
	d2 := newConnectDownload(t, sess)
	defer d1.cancel()
	defer d2.cancel()
	go d1.connectLoop()
	go d2.connectLoop()

	addr1 := unreachableAddr(t)
	addr2 := unreachableAddr(t)
	d1.peerList.addPeer(addr1, tracker.PeerSourceTracker)
	d2.peerList.addPeer(addr2, tracker.PeerSourceTracker)
	d1.signalConnect()
	d2.signalConnect()

	require.Eventually(t, func() bool {
		return candidateDialing(d1, addr1) && candidateDialing(d2, addr2)
	}, time.Second, 10*time.Millisecond)

	sess.ConnCount.Sub(1)
	sess.ConnSem.Release(1)

	require.Eventually(t, func() bool { return candidateFailed(d1, addr1) }, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return candidateFailed(d2, addr2) }, time.Second, 10*time.Millisecond)
}

// TestPeerIntakeWakesConnectLoop verifies the decoupled intake path: peers
// arriving on peersCh are added to the peer list and wake the connection
// loop, which then dials them.
func TestPeerIntakeWakesConnectLoop(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)

	go d.connectLoop()
	go d.startPeerIntake()

	addr := unreachableAddr(t)
	d.peersCh <- []tracker.DiscoveredPeer{{AddrPort: addr, Source: tracker.PeerSourceTracker}}

	require.Eventually(t, func() bool { return candidateFailed(d, addr) }, 5*time.Second, 10*time.Millisecond)
}

// TestConnectLoopExitsOnClose verifies that closing the download tears down a
// registered peer connection and releases every DialSem/ConnSem slot.
func TestConnectLoopExitsOnClose(t *testing.T) {
	sess := newConnectSession(t, 4)
	d := newConnectDownload(t, sess)
	go d.connectLoop()

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// Complete the BitTorrent handshake so the peer registers, then park the
	// connection idle (the peer's read loop blocks until the download closes).
	handshakeErr := make(chan error, 1)
	go func() {
		c, aErr := ln.Accept()
		if aErr != nil {
			handshakeErr <- aErr
			return
		}
		h, hErr := proto.ReadHandshake(c)
		if hErr != nil {
			handshakeErr <- hErr
			return
		}
		handshakeErr <- proto.SendHandshake(c, h.InfoHash, testOutgoingPeerID, false)
		_, _ = io.Copy(io.Discard, c) // holds the connection until closed
	}()

	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	d.peerList.addPeer(addr, tracker.PeerSourceTracker)
	d.signalConnect()

	// The dial succeeds and the peer registers after the handshake.
	require.Eventually(t, func() bool { return d.peerList.Size() > 0 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, <-handshakeErr)

	// Closing the download force-closes the connection and releases every slot.
	d.Close()
	require.Eventually(t, func() bool { return d.peerList.Size() == 0 }, 5*time.Second, 10*time.Millisecond)
	require.True(t, sess.DialSem.TryAcquire(4), "DialSem slots must be released after close")
	sess.DialSem.Release(4)
	require.True(t, sess.ConnSem.TryAcquire(200), "ConnSem slots must be released after close")
	sess.ConnSem.Release(200)
}
