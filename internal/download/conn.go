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

// dispatchConnections dials candidates one at a time. DialSem slots are held
// by the async dial goroutines until they finish, so a single download can
// have multiple dials in flight (up to DialSem capacity) while other
// downloads' pending Acquire calls still get served between our dials.
func (d *Download) dispatchConnections() {
	for {
		if !d.IsActive() || d.peerCount() >= d.maxConnections() {
			return
		}

		// Global connection pool full: stop dispatching. Without this short
		// circuit, dials would fail immediately with errConnectionLimit and
		// the loop would spin — each candidate is restored via clearDialing
		// and immediately re-picked. recordDisconnect wakes the loop when a
		// slot frees up.
		if int(d.session.ConnCount.Load()) >= int(d.session.Config.App.GlobalConnectionLimit) {
			return
		}

		if err := d.session.DialSem.Acquire(d.ctx, 1); err != nil {
			return // download closed; DialSem unchanged on ctx cancel
		}

		// State may have changed while blocked on DialSem; re-check before
		// committing a dial.
		if !d.IsActive() || d.peerCount() >= d.maxConnections() {
			d.session.DialSem.Release(1)
			return
		}

		candidates := d.peerList.connectPeers(time.Now().Unix(), 1)
		if len(candidates) == 0 {
			d.session.DialSem.Release(1)
			return // no candidates; re-woken when new peers arrive
		}

		go d.tryDial(candidates[0])
	}
}

// peerConn is an established peer transport connection.
type peerConn struct {
	conn      net.Conn
	encrypted bool
}

// errConnectionLimit is reported when the global connection slot is exhausted.
var errConnectionLimit = errors.New("connection limit reached")

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

// connectPeer establishes a full connection to a peer: TCP dial plus an
// optional MSE handshake. The caller holds DialSem.
//
// The returned peerConn owns a global connection slot; on success the slot
// ownership transfers with the conn to the registered peer (released by
// recordDisconnect). On failure the slot is released automatically.
//
// In prefer mode a failed MSE handshake closes the polluted connection and
// retries plaintext on a fresh TCP connection: the old connection's byte
// stream already contains MSE handshake data, so a plaintext handshake on
// it can never succeed.
func (d *Download) connectPeer(ctx context.Context, pp *persistentPeer) (peerConn, error) {
	// Grab a global connection slot before dialing.
	if !d.session.ConnSem.TryAcquire(1) {
		return peerConn{}, errConnectionLimit
	}
	d.session.ConnCount.Add(1)

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

// tryDial attempts to establish a connection to a candidate peer.
// DialSem is held by the caller (dispatchConnections); it is released on
// every exit path so the next dial can start.
// On success, registers the connection in the peer list.
// On failure, increments failcount.
func (d *Download) tryDial(pp *persistentPeer) {
	defer d.session.DialSem.Release(1)

	ctx, cancel := context.WithTimeout(d.ctx, peerConnectTimeout)
	defer cancel()

	d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

	pc, err := d.connectPeer(ctx, pp)
	if err != nil {
		if errors.Is(err, errConnectionLimit) {
			// The global connection pool was full by the time we dialed (e.g.
			// an incoming connection raced us for the last slot). This is our
			// own capacity limit, not a peer failure: restore the candidate
			// without penalizing its failcount, and rely on the ConnCount
			// short-circuit in dispatchConnections to stop further attempts
			// until a connection closes.
			d.peerList.clearDialing(pp)
			return
		}
		d.peerList.incFailcount(pp, err.Error())
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
// When the download is pending (queued), all peers are disconnected to free
// global connection slots for active downloads.
func (d *Download) peerTurnover() {
	peerCount := d.peers.Size()
	if peerCount == 0 {
		return
	}

	// Pending (queued) downloads don't need any peers — disconnect all to
	// free global connection slots. Peers will be reconnected when the
	// download is promoted back to Downloading.
	if d.HasState(PendingDownloading) {
		d.peers.Range(func(_ uint64, p Peer) bool {
			p.Close()
			return true
		})
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
	d.bannedAddrsMu.Lock()
	d.bannedAddrs[addr] = time.Now().Add(addrBanDuration)
	d.bannedAddrsMu.Unlock()
}
