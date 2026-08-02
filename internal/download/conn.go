// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/trim21/errgo"

	"neptune/internal/mse"
	"neptune/internal/pkg/global"
	"neptune/internal/proto"
)

const (
	peerConnectTimeout = 60 * time.Second
	addrBanDuration    = 24 * time.Hour
)

// AddConn adds an incoming connection from the listener.
// Ownership of the global connection semaphore slot and connection counter
// transfers to the download on success; on rejection, both are released.
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake, encrypted bool) {
	if d.isAddrBanned(addr.Addr()) {
		d.session.ConnSem.Release(1)
		d.session.ConnCount.Sub(1)
		conn.Close()
		return
	}
	if d.peers.Size() >= d.maxConnections() {
		d.session.ConnSem.Release(1)
		d.session.ConnCount.Sub(1)
		conn.Close()
		return
	}
	d.peerList.incomingConnectionOpened(addr)
	NewIncomingPeer(conn, d, addr, h, encrypted)
}

// connectLoop is the per-download connection driver. It dispatches dials for
// candidate peers until there is nothing left to do, then sleeps until an
// event (new peers, a closed connection, a state change) wakes it.
//
// Fairness across downloads comes from DialSem itself: Acquire is FIFO, so
// when the global dial slots are exhausted, competing downloads queue up and
// are served in arrival order — one download whose peers are all slow to
// time out can no longer starve others out of the slots. The download's own
// ctx bounds the loop, so closing the download releases everything (no
// goroutine leak, no dialing flag leak).
//
// The periodic ticker mirrors libtorrent's on_tick connection pass: peer
// candidates become eligible again purely as a function of time (failcount
// backoff in findConnectCandidates), so time-driven retries need a periodic
// wakeup, not just events. The interval is long enough that the loop is
// otherwise event-driven and stays quiet when there is nothing to do.
func (d *Download) connectLoop() {
	reconnectTicker := time.NewTicker(30 * time.Second)
	defer reconnectTicker.Stop()

	for {
		d.dispatchConnections()

		select {
		case <-d.ctx.Done():
			return
		case <-d.connectSignal:
		case <-reconnectTicker.C:
		}
	}
}

// dispatchConnections dispatches up to the current per-torrent free capacity.
// Candidates are selected before waiting on DialSem, so downloads with no work
// do not occupy positions in its FIFO wait queue.
func (d *Download) dispatchConnections() {
	for {
		if !d.IsActive() || d.occupiedConnectionCount() >= d.maxConnections() {
			return
		}

		now := time.Now().Unix()
		candidates := d.peerList.connectPeers(now, 1)
		if len(candidates) == 0 {
			return
		}
		candidate := candidates[0]

		// Re-check after peerList unlocks: an incoming connection or ban may
		// have raced candidate selection.
		if !d.peerList.canDial(candidate, now) {
			d.peerList.clearDialing(candidate)
			continue
		}

		if err := d.session.DialSem.Acquire(d.ctx, 1); err != nil {
			d.peerList.clearDialing(candidate)
			return
		}

		go func() {
			defer d.session.DialSem.Release(1)
			d.tryDial(candidate)
		}()
	}
}

// peerConn is an established peer transport connection.
type peerConn struct {
	conn      net.Conn
	encrypted bool
}

// dial establishes a TCP connection to addr, configuring deadline and linger.
func (d *Download) dial(ctx context.Context, addr netip.AddrPort) (net.Conn, error) {
	conn, err := global.Dial(ctx, "tcp", addr.String())
	if err != nil {
		return nil, err
	}

	_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

	if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
		_ = tcp.SetLinger(0)
	}

	return conn, nil
}

// connectPeerWithReservedSlot establishes a full connection to a peer: TCP
// dial plus an optional MSE handshake, using a ConnSem slot already reserved
// and counted by the caller. It transfers that slot to the caller (and
// eventually the registered peer) on success, or releases it on every failure
// path. The caller holds DialSem.
//
// In prefer mode a failed MSE handshake closes the polluted connection and
// retries plaintext on a fresh TCP connection: the old connection's byte
// stream already contains MSE handshake data, so a plaintext handshake on
// it can never succeed.
func (d *Download) connectPeerWithReservedSlot(ctx context.Context, pp *persistentPeer) (peerConn, error) {
	// Every failure path releases the slot; success transfers ownership.
	owned := false
	defer func() {
		if !owned {
			d.session.ConnSem.Release(1)
			d.session.ConnCount.Sub(1)
		}
	}()

	conn, err := d.dial(ctx, pp.addrPort)
	if err != nil {
		return peerConn{}, err
	}

	if !d.session.MSEEnabled {
		owned = true
		return peerConn{conn: conn}, nil
	}

	mseConn, method, mseErr := mse.NewConnection([]byte(d.info.Hash.AsString()), conn, d.session.MSEPreferredCrypto)
	if mseErr == nil {
		owned = true
		return peerConn{conn: mseConn, encrypted: method == mse.CryptoMethodRC4}, nil
	}

	// MSE handshake failed; the connection's stream is polluted.
	_ = conn.Close()

	if d.session.MSEForce {
		return peerConn{}, errgo.Wrap(mseErr, "mse handshake failed")
	}

	// Prefer mode: fall back to plaintext on a fresh connection.
	plainConn, err := d.dial(ctx, pp.addrPort)
	if err != nil {
		return peerConn{}, fmt.Errorf("mse: %w; plaintext dial: %v", mseErr, err)
	}
	owned = true
	return peerConn{conn: plainConn}, nil
}

// tryDial waits fairly for a global connection slot, then establishes a
// connection to a candidate peer. DialSem is held while waiting, which bounds
// the number of ConnSem waiters and preserves FIFO scheduling across downloads.
func (d *Download) tryDial(pp *persistentPeer) {
	// ConnSem represents the actual scarce resource. Blocking here rather
	// than failing fast makes the next released connection slot go to the
	// longest-waiting dial, instead of only waking its owning download.
	if err := d.session.ConnSem.Acquire(d.ctx, 1); err != nil {
		d.peerList.clearDialing(pp)
		return
	}
	d.session.ConnCount.Add(1)

	// The download state or candidate may have changed while waiting for the
	// global slot. Do not start a stale dial, and return ownership immediately.
	if !d.IsActive() || !d.peerList.canDial(pp, time.Now().Unix()) {
		d.peerList.clearDialing(pp)
		d.session.ConnSem.Release(1)
		d.session.ConnCount.Sub(1)
		d.signalConnect()
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, peerConnectTimeout)
	defer cancel()

	d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

	pc, err := d.connectPeerWithReservedSlot(ctx, pp)
	if err != nil {
		d.peerList.incFailcount(pp, err.Error())
		// The failed dial clears the candidate's dialing flag, freeing this
		// download's in-flight slot: wake the loop to dispatch the next one.
		d.signalConnect()
		return
	}

	p := NewOutgoingPeer(pc.conn, d, pp.addrPort, pc.encrypted)
	// Register the connection in the persistent peer list.
	d.peerList.newConnection(pp.addrPort, p, time.Now().Unix())
}

// recordDisconnect is called by Peer.Close() to clean up shared peer tracking.
// The connectedAddrs/peerList part is skipped if p is not the primary peer
// for its address (e.g. when a replacement has already arrived).
func (d *Download) recordDisconnect(p Peer) {
	if p.Incoming() {
		d.peerList.incomingConnectionClosed(p.Addr())
	}
	if actual, ok := d.connectedAddrs.Load(p.Addr()); ok && actual == p {
		d.connectedAddrs.Delete(p.Addr())

		failed := p.CloseError() != nil &&
			!errors.Is(p.CloseError(), io.EOF) &&
			!errors.Is(p.CloseError(), context.Canceled)

		if !p.Incoming() {
			d.peerList.connectionClosed(p.Addr(), time.Now().Unix(), p.HadTransfer(), failed)
		}
	}

	d.peers.Delete(p.ID())
	d.session.ConnSem.Release(1)
	d.session.ConnCount.Sub(1)

	// The freed global connection slot (and possibly a freed per-torrent
	// slot) lets this download dispatch a new candidate immediately.
	d.signalConnect()

	// Notify scheduler: blocks freed by abortDownload are now available
	// for other peers to pick up immediately.
	d.notifyPeersToRequest()
}

// EvictPeers disconnects up to n lowest-scored peers to free connection
// slots, limited by the number of connect candidates and skipping peers
// exempt from turnover (fast transfers). Returns the number evicted.
// Exported for the session-level (global) turnover scheduler.
func (d *Download) EvictPeers(n int) int {
	n = min(n, d.peerList.numCandidates())
	if n <= 0 {
		return 0
	}

	weAreSeed := d.HasState(Seeding)

	// Collect all connected peers and score them for turnover.
	type scoredPeer struct {
		p     Peer
		score int64
	}
	var scored []scoredPeer
	d.peers.Range(func(_ uint64, p Peer) bool {
		if !p.Closed() {
			scored = append(scored, scoredPeer{
				p:     p,
				score: peerDisconnectScore(p, weAreSeed),
			})
		}
		return true
	})

	slices.SortFunc(scored, func(a, b scoredPeer) int {
		if a.score < b.score {
			return -1
		}
		if a.score > b.score {
			return 1
		}
		return 0
	})

	evicted := 0
	for i := range min(n, len(scored)) {
		if scored[i].score >= turnoverExemptScore {
			// Sorted ascending — the remaining peers are all exempt.
			break
		}
		scored[i].p.Close()
		evicted++
	}
	return evicted
}

// peerTurnover disconnects least useful peers to make room for fresh candidates.
// Mirrors libtorrent's optimistic disconnect (~2% per round).
func (d *Download) peerTurnover() {
	peerCount := d.peers.Size()
	if peerCount == 0 {
		return
	}

	// Only turn over connections when approaching the per-torrent connection
	// limit (TurnoverCutoff%): eviction only makes sense to free a slot for
	// a new candidate. Below the cutoff, new candidates connect directly
	// without disrupting peers. Mirrors libtorrent's peer_turnover_cutoff.
	maxConn := d.maxConnections()
	if maxConn < 6 || peerCount < maxConn*TurnoverCutoff/100 {
		return
	}

	const turnoverFraction = 100 / 4 // 4% of peers, mirrors libtorrent's peer_turnover
	disconnectN := max(peerCount/turnoverFraction, 1)
	d.EvictPeers(disconnectN)
}

// isAddrBanned checks whether an address is currently banned for this torrent.
func (d *Download) isAddrBanned(addr netip.Addr) bool {
	d.bannedAddrsMu.Lock()
	defer d.bannedAddrsMu.Unlock()

	expires, ok := d.bannedAddrs[addr]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(d.bannedAddrs, addr)
		return false
	}
	return true
}

// banAddr bans an address from connecting to this torrent for addrBanDuration.
func (d *Download) banAddr(addr netip.Addr) {
	expires := time.Now().Add(addrBanDuration)
	d.bannedAddrsMu.Lock()
	d.bannedAddrs[addr] = expires
	d.bannedAddrsMu.Unlock()
	d.peerList.banAddr(addr, expires)
}
