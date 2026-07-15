// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"

	"neptune/internal/mse"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/global"
	"neptune/internal/proto"
)

const (
	peerConnectTimeout = 15 * time.Second
	addrBanDuration    = 24 * time.Hour
)

// AddConn adds an incoming connection from the listener.
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake, encrypted bool) {
	if d.isAddrBanned(addr.Addr()) {
		conn.Close()
		return
	}
	if d.peers.Size() >= d.maxConnections() {
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
			if !d.session.ConnSem.TryAcquire(1) {
				d.peerList.clearDialing(candidate)
				semFull = true
				// Continue the inner loop to clearDialing remaining
				// candidates, then stop: no point retrying until slots free up.
				continue
			}
			d.session.ConnCount.Add(1)
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

// tryDial attempts a TCP connect to a candidate peer.
// On success, registers the connection in the peer list.
// On failure, increments failcount and releases the semaphore.
func (d *Download) tryDial(pp *persistentPeer) {
	ctx, cancel := context.WithTimeout(d.ctx, peerConnectTimeout)
	defer cancel()

	d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

	conn, err := global.Dial(ctx, "tcp", pp.addrPort.String())
	if err != nil {
		d.peerList.incFailcount(pp, err.Error())
		d.session.ConnSem.Release(1)
		d.session.ConnCount.Sub(1)
		// Wake up connection loop to try next candidate.
		select {
		case d.pendingPeersSignal <- empty.Empty{}:
		default:
		}
		return
	}

	_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

	if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
		_ = tcp.SetLinger(0)
	}

	var encrypted bool
	if d.session.MSEEnabled {
		infoHash := d.info.Hash.AsString()
		mseConn, method, mseErr := mse.NewConnection([]byte(infoHash), conn, d.session.MSEPreferredCrypto)
		if mseErr != nil {
			if d.session.MSEForce {
				_ = conn.Close()
				d.peerList.incFailcount(pp, mseErr.Error())
				d.session.ConnSem.Release(1)
				d.session.ConnCount.Sub(1)
				select {
				case d.pendingPeersSignal <- empty.Empty{}:
				default:
				}
				return
			}
			// prefer mode: MSE failed, fall back to plain connection.
			// conn was not consumed by MSE on failure, reuse it.
		} else {
			conn = mseConn
			encrypted = method == mse.CryptoMethodRC4
		}
	}

	p := NewOutgoingPeer(conn, d, pp.addrPort, encrypted)
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

		d.peerList.connectionClosed(p.Addr(), time.Now().Unix(), p.HadTransfer(), failed)
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
	toDisconnect := d.peerList.peerTurnover(disconnectN, weAreSeed)
	for _, p := range toDisconnect {
		p.Close()
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
