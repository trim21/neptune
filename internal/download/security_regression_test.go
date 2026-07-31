// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
	"neptune/internal/proto"
)

// TestValidateRequestRejectsOverflowedBegin is a regression test for the
// uint32 overflow in validateRequest: Begin+Length wrapped around and
// bypassed the piece-size check, letting a peer request arbitrary file
// offsets (out-of-bounds read / memory disclosure).
func TestValidateRequestRejectsOverflowedBegin(t *testing.T) {
	d := newTestDownload(t, 640, 1024, piece_store.NewMemStore) // 10 GiB torrent, 16 MiB pieces
	p := &peerImpl{d: d}

	pieceSize := d.info.PieceLen(5) // 16 MiB
	require.Equal(t, int64(16*1024*1024), pieceSize)

	tests := []struct {
		name string
		req  proto.ChunkRequest
		want bool
	}{
		{
			name: "overflowing begin+length bypasses check",
			req:  proto.ChunkRequest{PieceIndex: 5, Begin: 0xFFFF8000, Length: 0x8000},
			want: false,
		},
		{
			name: "overflowing begin alone",
			req:  proto.ChunkRequest{PieceIndex: 5, Begin: 0xFFFFFFF0, Length: 0x10000},
			want: false,
		},
		{
			name: "valid first block",
			req:  proto.ChunkRequest{PieceIndex: 5, Begin: 0, Length: 0x4000},
			want: true,
		},
		{
			name: "valid last block exactly at piece end",
			req:  proto.ChunkRequest{PieceIndex: 5, Begin: uint32(pieceSize - 0x4000), Length: 0x4000},
			want: true,
		},
		{
			name: "request past end of piece",
			req:  proto.ChunkRequest{PieceIndex: 5, Begin: uint32(pieceSize - 0x4000 + 1), Length: 0x4000},
			want: false,
		},
		{
			name: "piece index out of range",
			req:  proto.ChunkRequest{PieceIndex: d.info.NumPieces, Begin: 0, Length: 0x4000},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, p.validateRequest(tt.req))
		})
	}
}

// TestDecodeEventsRejectsShortMessages is a regression test for stream
// desync and the size-2 uint32 underflow: a size=1 Extended message made
// decodeEvents allocate ~4 GiB (make([]byte, size-2)), and undersized
// Request/Have/Port messages made decoders consume bytes from the next
// message, desynchronizing the stream.
func TestDecodeEventsRejectsShortMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte // size prefix + message id
	}{
		{name: "extended size=1", msg: []byte{0, 0, 0, 1, byte(proto.Extended)}},
		{name: "request size=1", msg: []byte{0, 0, 0, 1, byte(proto.Request)}},
		{name: "cancel size=1", msg: []byte{0, 0, 0, 1, byte(proto.Cancel)}},
		{name: "reject size=1", msg: []byte{0, 0, 0, 1, byte(proto.Reject)}},
		{name: "have size=1", msg: []byte{0, 0, 0, 1, byte(proto.Have)}},
		{name: "suggest size=1", msg: []byte{0, 0, 0, 1, byte(proto.Suggest)}},
		{name: "allowed_fast size=1", msg: []byte{0, 0, 0, 1, byte(proto.AllowedFast)}},
		{name: "port size=1", msg: []byte{0, 0, 0, 1, byte(proto.Port)}},
		{name: "unchoke size=2", msg: []byte{0, 0, 0, 2, byte(proto.Unchoke), 0}},
		{name: "extended size=1 followed by null byte", msg: []byte{0, 0, 0, 1, byte(proto.Extended), 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()

			p := &peerImpl{
				Conn: local,
				r:    bufio.NewReaderSize(local, 4096),
			}

			_, err := remote.Write(tt.msg)
			require.NoError(t, err)

			var ev Event
			err = p.decodeEvents(&ev)
			require.ErrorIs(t, err, ErrPeerSendInvalidData)
		})
	}
}

// TestDecodeEventsAcceptsWellFormedRequest ensures the length check does not
// reject valid messages.
func TestDecodeEventsAcceptsWellFormedRequest(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()

	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	p := &peerImpl{
		Conn: local,
		r:    bufio.NewReaderSize(local, 4096),
		d:    d,
	}

	// size=13: Request { piece 0, begin 0, length 0x4000 }
	msg := make([]byte, 0, 17)
	msg = binary.BigEndian.AppendUint32(msg, 13)
	msg = append(msg, byte(proto.Request))
	msg = binary.BigEndian.AppendUint32(msg, 0)
	msg = binary.BigEndian.AppendUint32(msg, 0)
	msg = binary.BigEndian.AppendUint32(msg, 0x4000)

	_, err := remote.Write(msg)
	require.NoError(t, err)

	var ev Event
	err = p.decodeEvents(&ev)
	require.NoError(t, err)
	require.Equal(t, proto.Request, ev.Event)
	require.Equal(t, proto.ChunkRequest{PieceIndex: 0, Begin: 0, Length: 0x4000}, ev.Req)
}

// TestSetErrorDoesNotPanicOnEOF is a regression test: PieceStore.ReadChunk
// surfaces a bare io.EOF when the backing file is truncated (disk full,
// external modification). setError used to panic on it, crashing the whole
// process; it must instead transition to Error state.
func TestSetErrorDoesNotPanicOnEOF(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.state.Store(uint32(Downloading))

	require.NotPanics(t, func() {
		d.setError(io.EOF)
	})

	require.NotEmpty(t, d.ErrorMsg())
	require.Equal(t, Error, d.GetState())
	assert.Contains(t, d.ErrorMsg(), "EOF")
}
