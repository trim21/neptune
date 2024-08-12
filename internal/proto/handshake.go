// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/assert"
	"neptune/internal/pkg/ro"
)

func genReversedFlag(index int, value byte) uint64 {
	var b [8]byte
	b[index] = value
	return binary.BigEndian.Uint64(b[:])
}

var handshakePstrV1 = ro.S("\x13BitTorrent protocol")

// https://www.bittorrent.org/beps/bep_0005.html
// reserved_byte[7] & 0x01.
var dhtEnabled = genReversedFlag(7, 0x01)

// https://www.bittorrent.org/beps/bep_0006.html
// reserved_byte[7] & 0x04.
var fastExtensionEnabled = genReversedFlag(7, 0x04)

// https://www.bittorrent.org/beps/bep_0010.html
// reserved_byte[5] & 0x10.
var exchangeExtensionEnabled = genReversedFlag(5, 0x10)

var privateHandshakeBytes = ro.B(binary.BigEndian.AppendUint64(nil, exchangeExtensionEnabled|fastExtensionEnabled))
var publicHandshakeBytes = ro.B(binary.BigEndian.AppendUint64(nil, exchangeExtensionEnabled|fastExtensionEnabled|dhtEnabled))

// SendHandshake = <pStrlen><pStr><reserved><info_hash><peer_id>
// - pStrlen = length of pStr (1 byte)
// - pStr = string identifier of the protocol: "BitTorrent protocol" (19 bytes)
// - reserved = 8 reserved bytes indicating extensions to the protocol (8 bytes)
// - info_hash = hash of the value of the 'info' key of the torrent file (20 bytes)
// - peer_id = unique identifier of the Peer (20 bytes)
//
// Total length = payload length = 49 + len(pstr) = 68 bytes (for BitTorrent v1).
func SendHandshake(conn io.Writer, infoHash, peerID [20]byte, private bool) error {
	_, err := handshakePstrV1.WriteTo(conn)
	if err != nil {
		return err
	}

	if private {
		_, err = privateHandshakeBytes.WriteTo(conn)
	} else {
		_, err = publicHandshakeBytes.WriteTo(conn)
	}

	if err != nil {
		return err
	}

	_, err = conn.Write(infoHash[:])
	if err != nil {
		return err
	}

	_, err = conn.Write(peerID[:])
	return err
}

type Handshake struct {
	InfoHash           metainfo.Hash
	PeerID             PeerID
	FastExtension      bool
	ExchangeExtensions bool
	DhtEnabled         bool
}

func (h Handshake) GoString() string {
	return fmt.Sprintf("Handshake{InfoHash='%x', PeerID='%s'}", h.InfoHash, h.PeerID)
}

var ErrHandshakeMismatch = errors.New("handshake string mismatch")

func ReadHandshake(conn io.Reader) (Handshake, error) {
	var b = make([]byte, 20)
	n, err := io.ReadFull(conn, b)
	if err != nil {
		return Handshake{}, err
	}

	assert.Equal(20, n)

	if !handshakePstrV1.EqualBytes(b) {
		return Handshake{}, ErrHandshakeMismatch
	}

	_, err = io.ReadFull(conn, b[:8])
	if err != nil {
		return Handshake{}, err
	}

	reversed := binary.BigEndian.Uint64(b)

	var h = Handshake{}

	if reversed&fastExtensionEnabled != 0 {
		h.FastExtension = true
	}

	if reversed&exchangeExtensionEnabled != 0 {
		h.ExchangeExtensions = true
	}

	if reversed&dhtEnabled != 0 {
		h.DhtEnabled = true
	}

	n, err = io.ReadFull(conn, h.InfoHash[:])
	if err != nil {
		return Handshake{}, err
	}
	assert.Equal(20, n)

	n, err = io.ReadFull(conn, h.PeerID[:])
	if err != nil {
		return Handshake{}, err
	}

	assert.Equal(20, n)

	return h, nil
}
