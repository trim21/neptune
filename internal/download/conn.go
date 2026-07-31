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
	"neptune/internal/pkg/empty"
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

// connectToPeers tries to connect to candidate peers from the peer list.
// Mirrors libtorrent's torrent::try_connect_peer loop.
func (d *Download) connectToPeers(maxSlots int) int {
	now := time.Now().Unix()
	connected := 0

	for connected < maxSlots {
		remaining := maxSlots - connected
		candidates := d.peerList.connectPeers(now, remaining)
		if len(candidates) == 0 {
			break
		}

		semFull := false
		for _, candidate := range candidates {
			if semFull {
				d.peerList.clearDialing(candidate)
				continue
			}
			if _, ok := d.connectedAddrs.Load(candidate.addrPort); ok {
				d.peerList.clearDialing(candidate)
				continue
			}
			if d.isAddrBanned(candidate.addrPort.Addr()) {
				d.peerList.clearDialing(candidate)
				continue
			}
			if !d.session.DialSem.TryAcquire(1) {
				d.peerList.clearDialing(candidate)
				semFull = true
				// Continue the inner loop to clearDialing remaining
				// candidates, then stop: no point retrying until slots free up.
				continue
			}
			go d.tryDial(candidate)
			connected++
			if connected >= maxSlots {
				return connected
			}
		}

		if semFull {
			return connected
		}
	}

	return connected
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
// DialSem is held by the caller; it is released on every exit path.
// On success, registers the connection in the peer list.
// On failure, increments failcount.
func (d *Download) tryDial(pp *persistentPeer) {
	defer d.session.DialSem.Release(1)

	ctx, cancel := context.WithTimeout(d.ctx, peerConnectTimeout)
	defer cancel()

	d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

	pc, err := d.connectPeer(ctx, pp)
	if err != nil {
		// A full global connection slot is our own capacity limit, not a
		// failure of the peer — don't penalize the peer's failcount.
		if !errors.Is(err, errConnectionLimit) {
			d.peerList.incFailcount(pp, err.Error())
		}
		// Wake up connection loop to try next candidate.
		select {
		case d.pendingPeersSignal <- empty.Empty{}:
		default:
		}
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

	// Wake up connection loop to fill the freed slot.
	if d.IsActive() {
		select {
		case d.pendingPeersSignal <- empty.Empty{}:
		default:
		}
	}

	// Notify scheduler: blocks freed by abortDownload are now available
	// for other peers to pick up immediately.
	d.notifyPeersToRequest()
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

	// Only turn over connections when approaching the per-torrent limit
	// (>= 90%). Mirrors libtorrent's peer_turnover_cutoff logic.
	const turnoverCutoff = 90 // percent of connection limit

	maxConn := d.maxConnections()
	if maxConn < 6 || peerCount < maxConn*turnoverCutoff/100 {
		return
	}

	const turnoverFraction = 100 / 4 // 4% of peers, mirrors libtorrent's peer_turnover

	disconnectN := max(peerCount/turnoverFraction, 1)
	candidateN := d.peerList.numCandidates()
	disconnectN = min(disconnectN, candidateN)

	if disconnectN == 0 {
		return
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

	for i := range min(disconnectN, len(scored)) {
		scored[i].p.Close()
	}
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
