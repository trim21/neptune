// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"

	"neptune/internal/pkg/global"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/proto"
)

const (
	peerConnectTimeout = time.Minute
)

// AddConn adds an incoming connection from the listener.
// The peer entry is created in newPeer via addOrUpdateIncoming.
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake) {
	NewIncomingPeer(conn, d, addr, h)
}

// connectToPeers tries to connect to candidate peers from the peer list.
// Mirrors libtorrent's torrent::try_connect_peer loop.
func (d *Download) connectToPeers(maxSlots int) int {
	now := time.Now().Unix()
	connected := 0

	for connected < maxSlots {
		// 1. Try immediate (hadTrans) candidates first — fast reconnect
		candidate := d.peerList.immediateCandidate()
		if candidate == nil {
			candidate = d.peerList.connectOnePeer(now)
		}
		if candidate == nil {
			break
		}

		// Check if already connected
		if _, ok := d.connectedAddrs.Load(candidate.addrPort); ok {
			continue
		}

		// Check global connection limit
		if !d.c.sem.TryAcquire(1) {
			break
		}
		d.c.connectionCount.Add(1)

		tasks.Submit(func() {
			d.tryDial(candidate)
		})

		connected++
	}

	return connected
}

// tryDial attempts a TCP connect to a candidate peer.
// On success, registers the connection in the peer list.
// On failure, increments failcount and releases the semaphore.
func (d *Download) tryDial(pp *persistentPeer) {
	ctx, cancel := context.WithTimeout(context.Background(), peerConnectTimeout)
	defer cancel()

	d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

	conn, err := global.Dial(ctx, "tcp", pp.addrPort.String())
	if err != nil {
		pp.lastErr = err.Error()
		d.peerList.incFailcount(pp)
		d.c.sem.Release(1)
		d.c.connectionCount.Sub(1)
		return
	}

	_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

	if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
		_ = tcp.SetLinger(0)
	}

	p := NewOutgoingPeer(conn, d, pp.addrPort)
	// Register the connection in the persistent peer list.
	d.peerList.newConnection(pp.addrPort, p, time.Now().Unix())
}

// recordDisconnect is called by Peer.close() to update shared peer tracking.
// It only acts if p is the primary peer for its address (registered in connectedAddrs).
func (d *Download) recordDisconnect(p *Peer) {
	if actual, ok := d.connectedAddrs.Load(p.Address); !ok || actual != p {
		return
	}
	d.connectedAddrs.Delete(p.Address)

	failed := p.closeErr != nil &&
		!errors.Is(p.closeErr, io.EOF) &&
		!errors.Is(p.closeErr, context.Canceled)

	d.peerList.connectionClosed(p.Address, time.Now().Unix(), p.hadTransfer, failed)
}

// peerTurnover disconnects slow peers to make room for fresh candidates.
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

	toDisconnect := d.peerList.peerTurnover(disconnectN)
	for _, p := range toDisconnect {
		p.close()
	}
}
