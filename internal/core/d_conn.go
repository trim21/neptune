// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"time"

	"neptune/internal/pkg/global"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/proto"
)

type connHistory struct {
	lastTry time.Time
	err     error
	timeout bool
	refused bool
}

// AddConn add an incoming connection from client listener
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake) {
	//d.connMutex.Lock()
	//defer d.connMutex.Unlock()
	d.connectionHistory.Add(addr, connHistory{})
	NewIncomingPeer(conn, d, addr, h)
}

func (d *Download) connectToPeers() {
	d.pendingPeersMutex.Lock()
	defer d.pendingPeersMutex.Unlock()

	for d.pendingPeers.Len() > 0 {
		// try connecting first
		pp := d.pendingPeers.Pop()

		if item, ok := d.connectionHistory.Get(pp.addrPort); ok {
			if item.timeout {
				continue
			}
			if item.refused {
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
				if errors.Is(err, context.DeadlineExceeded) {
					ch.timeout = true
				} else if errors.Is(err, syscall.ECONNREFUSED) {
					ch.refused = true
				} else {
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
