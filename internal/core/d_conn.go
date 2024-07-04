// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"

	"tyr/internal/mse"
	"tyr/internal/pkg/global"
	"tyr/internal/pkg/global/tasks"
	"tyr/internal/proto"
)

// AddConn add an incoming connection from client listener
func (d *Download) AddConn(addr netip.AddrPort, conn net.Conn, h proto.Handshake) {
	//d.connMutex.Lock()
	//defer d.connMutex.Unlock()
	d.connectionHistory.Store(addr, connHistory{})
	d.conn.Store(addr, NewIncomingPeer(conn, d, addr, h))
}

func (d *Download) connectToPeers() {
	d.peersMutex.Lock()
	defer d.peersMutex.Unlock()

	for d.peers.Len() > 0 {
		// try connecting first
		pp := d.peers.Peek()

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
			d.peers.Pop()
			continue
		}

		if !d.c.sem.TryAcquire(1) {
			break
		}
		d.c.connectionCount.Add(1)

		// actually remove it
		d.peers.Pop()

		tasks.Submit(func() {
			ch := connHistory{lastTry: time.Now()}
			defer func(h connHistory) {
				d.c.ch.Set(pp.addrPort, h, time.Hour)
			}(ch)

			ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			conn, err := global.Dialer.DialContext(ctx, "tcp", pp.addrPort.String())
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					ch.timeout = true
				} else {
					ch.err = err
				}
				d.c.sem.Release(1)
				d.c.connectionCount.Sub(1)
				return
			}

			if d.c.mseDisabled {
				d.conn.Store(pp.addrPort, NewOutgoingPeer(conn, d, pp.addrPort))
				return
			}

			rwc, err := mse.NewConnection(d.info.Hash.Bytes(), conn)
			if err != nil {
				ch.err = err
				d.c.sem.Release(1)
				d.c.connectionCount.Sub(1)
				return
			}

			d.conn.Store(pp.addrPort, NewOutgoingPeer(rwc, d, pp.addrPort))
		})
	}
}
