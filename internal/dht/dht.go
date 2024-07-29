// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// TODO: implement dht

package dht

import (
	"net"
	"net/netip"
	"sync"
)

func Start(conn net.PacketConn, port uint16) *DHT {
	return &DHT{
		Port: port,
		conn: conn,
	}
}

type DHT struct {
	conn       net.PacketConn
	peers      []netip.AddrPort
	peersMutex sync.RWMutex
	Port       uint16
}

func (d *DHT) AddPeer(p netip.AddrPort) {
	d.peersMutex.Lock()
	defer d.peersMutex.Unlock()

	d.peers = append(d.peers, p)
}
