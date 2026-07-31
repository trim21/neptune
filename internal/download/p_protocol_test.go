// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"bufio"
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
	"neptune/internal/proto"
)

// fakeConn only needs SetReadDeadline for decodeEvents; the reader is a bufio
// reader fed directly from a bytes.Buffer.
type fakeDecodeConn struct {
	net.Conn
}

func (fakeDecodeConn) SetReadDeadline(time.Time) error { return nil }

func newDecodePeer(t *testing.T, data []byte) (*peerImpl, *Event) {
	t.Helper()

	d := newTestDownload(t, 4, 4, piece_store.NewMemStore)
	p := &peerImpl{
		d:       d,
		Conn:    fakeDecodeConn{},
		r:       bufio.NewReader(bytes.NewReader(data)),
		readBuf: [4]byte{},
	}
	return p, &Event{}
}

// TestDecodeEventsRejectsOversizedMalformedMessages verifies that messages
// whose declared size does not match their type are rejected instead of
// desynchronizing the stream or underflowing size arithmetic.
func TestDecodeEventsRejectsMalformedSize(t *testing.T) {
	// size=1 Extended (only the message id). size-2 used to underflow and
	// allocate/discard ~4 GiB.
	p, ev := newDecodePeer(t, []byte{0, 0, 0, 1, byte(proto.Extended)})
	err := p.decodeEvents(ev)
	require.ErrorIs(t, err, ErrPeerSendInvalidData)

	// size=1 Request: used to read 12 bytes across the message boundary,
	// desynchronizing the stream.
	p, ev = newDecodePeer(t, []byte{0, 0, 0, 1, byte(proto.Request)})
	err = p.decodeEvents(ev)
	require.ErrorIs(t, err, ErrPeerSendInvalidData)

	// Have with a wrong payload size (1+5 instead of 1+4).
	p, ev = newDecodePeer(t, []byte{0, 0, 0, 6, byte(proto.Have), 0, 0, 0, 5, 0})
	err = p.decodeEvents(ev)
	require.ErrorIs(t, err, ErrPeerSendInvalidData)
}

func TestDecodeEventsValidMessages(t *testing.T) {
	// Valid Have message with piece index 5.
	p, ev := newDecodePeer(t, []byte{0, 0, 0, 5, byte(proto.Have), 0, 0, 0, 5})
	require.NoError(t, p.decodeEvents(ev))
	require.Equal(t, proto.Have, ev.Event)
	require.Equal(t, uint32(5), ev.Index)

	// Valid Choke (size 1).
	p, ev = newDecodePeer(t, []byte{0, 0, 0, 1, byte(proto.Choke)})
	require.NoError(t, p.decodeEvents(ev))
	require.Equal(t, proto.Choke, ev.Event)

	// Keep-alive (size 0).
	p, ev = newDecodePeer(t, []byte{0, 0, 0, 0})
	require.NoError(t, p.decodeEvents(ev))
	require.True(t, ev.keepAlive)

	// Valid Request: piece 0, begin 0, length 16KiB.
	req := []byte{0, 0, 0, 13, byte(proto.Request), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00}
	p, ev = newDecodePeer(t, req)
	require.NoError(t, p.decodeEvents(ev))
	require.Equal(t, proto.Request, ev.Event)
	require.Equal(t, uint32(0x4000), ev.Req.Length)
}
