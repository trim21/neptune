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
)

// AddConn adds an incoming connection from the listener.
// The peer entry is created in newPeer via addOrUpdateIncoming.
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake, encrypted bool) {
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
func (d *Download) peerTurnover() {
	const turnoverFraction = 50 // 1/50 = 2%
	peerCount := d.peers.Size()
	if peerCount == 0 {
		return
	}

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
