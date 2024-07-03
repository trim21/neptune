package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/negrel/assert"

	"tyr/internal/meta"
	"tyr/internal/pkg/ro"
)

func genReversedFlag(index int, value byte) uint64 {
	var b [8]byte
	b[index] = value
	return binary.BigEndian.Uint64(b[:])
}

var handshakePstrV1 = ro.S("\x13BitTorrent protocol")

// https://www.bittorrent.org/beps/bep_0006.html
// reserved_byte[7] & 0x04
var fastExtensionEnabled = genReversedFlag(7, 0x04)

// https://www.bittorrent.org/beps/bep_0010.html
// reserved_byte[5] & 0x10
var exchangeExtensionEnabled = genReversedFlag(5, 0x10)

var handshakeBytes = ro.B(binary.BigEndian.AppendUint64(nil, exchangeExtensionEnabled|fastExtensionEnabled))

// SendHandshake = <pStrlen><pStr><reserved><info_hash><peer_id>
// - pStrlen = length of pStr (1 byte)
// - pStr = string identifier of the protocol: "BitTorrent protocol" (19 bytes)
// - reserved = 8 reserved bytes indicating extensions to the protocol (8 bytes)
// - info_hash = hash of the value of the 'info' key of the torrent file (20 bytes)
// - peer_id = unique identifier of the Peer (20 bytes)
//
// Total length = payload length = 49 + len(pstr) = 68 bytes (for BitTorrent v1)
func SendHandshake(conn io.Writer, infoHash, peerID [20]byte) error {
	_, err := handshakePstrV1.WriteTo(conn)
	if err != nil {
		return err
	}

	_, err = handshakeBytes.WriteTo(conn)
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
	InfoHash           meta.Hash
	PeerID             [20]byte
	FastExtension      bool
	ExchangeExtensions bool
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

	n, err = io.ReadFull(conn, b[:8])
	if err != nil {
		return Handshake{}, err
	}

	assert.Equal(8, n)

	reversed := binary.BigEndian.Uint64(b)

	var h = Handshake{}

	if reversed&fastExtensionEnabled != 0 {
		h.FastExtension = true
	}

	if reversed&exchangeExtensionEnabled != 0 {
		h.ExchangeExtensions = true
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
