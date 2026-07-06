// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"syscall"
	"time"

	"neptune/internal/pkg/global"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/proto"
)

// connBackoff constants mirror libtorrent's strategy:
//   - Timeout (transient network issue): short backoff, immediate if peer had transfers
//   - Refused (peer likely offline): long backoff
//   - Other errors: moderate backoff
const (
	connBackoffTimeout  = 30 * time.Second
	connBackoffRefused  = 10 * time.Minute
	connBackoffGeneric  = 1 * time.Minute
	connBackoffTransfer = 0 // immediate retry if peer had active transfers
)

type connDisconnectReason uint8

const (
	connReasonNone    connDisconnectReason = iota // never connected / no info
	connReasonEOF                                 // peer closed cleanly (io.EOF)
	connReasonTimeout                             // read deadline exceeded / TCP timeout
	connReasonRefused                             // connection refused (only for outgoing)
	connReasonError                               // other network error
)

type connHistory struct {
	lastTry  time.Time
	err      error
	hadTrans bool
	reason   connDisconnectReason
}

// AddConn add an incoming connection from client listener.
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake) {
	d.connectionHistory.Add(addr, connHistory{})
	NewIncomingPeer(conn, d, addr, h)
}

func (d *Download) connectToPeers() {
	d.pendingPeersMutex.Lock()
	defer d.pendingPeersMutex.Unlock()

	for d.pendingPeers.Len() > 0 {
		pp := d.pendingPeers.Pop()

		if item, ok := d.connectionHistory.Get(pp.addrPort); ok {
			if d.canRetry(item) {
				continue
			}
		}

		if _, ok := d.peers.Load(pp.addrPort); ok {
			continue
		}

		if !d.c.sem.TryAcquire(1) {
			break
		}
		d.c.connectionCount.Add(1)

		tasks.Submit(func() {
			ch := connHistory{lastTry: time.Now()}
			d.connectionHistory.Add(pp.addrPort, ch)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			d.log.Trace().Msgf("try to connect to peer %s", pp.addrPort)

			conn, err := global.Dial(ctx, "tcp", pp.addrPort.String())
			if err != nil {
				ch.lastTry = time.Now()
				if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
					ch.reason = connReasonTimeout
				} else if errors.Is(err, syscall.ECONNREFUSED) {
					ch.reason = connReasonRefused
				} else {
					ch.reason = connReasonError
					ch.err = err
				}

				d.connectionHistory.Add(pp.addrPort, ch)
				d.c.sem.Release(1)
				d.c.connectionCount.Sub(1)
				return
			}

			_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

			// peers is wrapped by conntrack, so we need to cast it with interface
			if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
				_ = tcp.SetLinger(0)
			}

			NewOutgoingPeer(conn, d, pp.addrPort)
		})
	}
}

// canRetry returns true if the peer should be skipped (not yet ready to retry).
// Returns false if enough time has passed to retry the connection.
func (d *Download) canRetry(ch connHistory) bool {
	// If peer had transferred data, retry immediately regardless of reason.
	// This mirrors libtorrent's behavior where peers with transfer_counter > 0
	// are never culled and can be reconnected aggressively.
	if ch.hadTrans {
		return false
	}

	elapsed := time.Since(ch.lastTry)

	switch ch.reason {
	case connReasonNone:
		// Never tried before, always allow.
		return false
	case connReasonTimeout:
		return elapsed < connBackoffTimeout
	case connReasonRefused:
		return elapsed < connBackoffRefused
	case connReasonEOF:
		// Peer closed cleanly — it may come back soon (e.g., client restart).
		// Use a short backoff.
		return elapsed < connBackoffTimeout
	default:
		return elapsed < connBackoffGeneric
	}
}

// recordDisconnect records the reason a peer disconnected.
// Called by Peer.close() to update connection history for future retry decisions.
func (d *Download) recordDisconnect(addr netip.AddrPort, hadTrans bool, err error) {
	ch := connHistory{
		lastTry:  time.Now(),
		hadTrans: hadTrans,
	}

	if err == nil {
		ch.reason = connReasonEOF
	} else if os.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		ch.reason = connReasonTimeout
	} else if errors.Is(err, io.EOF) {
		ch.reason = connReasonEOF
	} else if errors.Is(err, syscall.ECONNRESET) {
		ch.reason = connReasonError
	} else {
		ch.reason = connReasonError
		ch.err = err
	}

	d.connectionHistory.Add(addr, ch)
}
