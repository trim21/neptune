// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"tyr/internal/pkg/global"
	"tyr/internal/pkg/global/tasks"
	"tyr/internal/proto"
)

type connHistory struct {
	lastTry   time.Time
	err       error
	timeout   bool
	connected bool
}

// AddConn add an incoming connection from client listener
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake) {
	//d.connMutex.Lock()
	//defer d.connMutex.Unlock()
	d.connectionHistory.Store(addr, connHistory{})
	NewIncomingPeer(conn, d, addr, h)
}

func (d *Download) connectToPeers() {
	d.peersMutex.Lock()
	defer d.peersMutex.Unlock()

	for d.peers.Len() > 0 {
		// try connecting first
		pp := d.peers.Pop()

		if item := d.c.ch.Get(pp.addrPort); item != nil {
			ch := item.Value()
			if ch.timeout {
				continue
			}
			if ch.err != nil {
				continue
			}
		}

		if _, ok := d.conn.Load(pp.addrPort); ok {
			continue
		}

		if !d.c.sem.TryAcquire(1) {
			break
		}
		d.c.connectionCount.Add(1)

		tasks.Submit(func() {
			ch := connHistory{lastTry: time.Now()}
			defer func(h connHistory) {
				d.c.ch.Set(pp.addrPort, h, time.Hour)
			}(ch)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			d.log.Debug().Msgf("try to connect to peer %s", pp.addrPort)

			conn, err := global.Dial(ctx, "tcp", pp.addrPort.String())
			if err != nil {
				d.log.Debug().Err(err).Msgf("failed to connect to peer %s", pp.addrPort)
				if errors.Is(err, context.DeadlineExceeded) {
					ch.timeout = true
				} else {
					ch.err = err
				}
				d.c.sem.Release(1)
				d.c.connectionCount.Sub(1)
				return
			}

			_ = conn.SetDeadline(time.Now().Add(global.ConnTimeout))

			// conn is wrapped by conntrack, so we need to cast it with interface
			//if tcp, ok := conn.(interface{ SetLinger(sec int) error }); ok {
			//	_ = tcp.SetLinger(0)
			//}

			NewOutgoingPeer(conn, d, pp.addrPort)
			return
		})
	}
}
