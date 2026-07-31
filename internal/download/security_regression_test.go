// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"io"
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
	d := newTestDownload(t, 8, 4, piece_store.NewMemStore) // 64 KiB pieces
	p := &peerImpl{d: d}

	pieceSize := d.info.PieceLen(5)
	require.Equal(t, int64(64*1024), pieceSize)

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

// TestSetErrorDoesNotPanicOnEOF is a regression test: PieceStore.ReadChunk
// used to surface a bare io.EOF when the backing file is truncated (disk
// full, external modification); setError panicked on it, crashing the whole
// process. The store now normalizes truncated reads to
// io.ErrUnexpectedEOF, and setError must never panic regardless.
func TestSetErrorDoesNotPanicOnEOF(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.state.Store(uint32(Downloading))

	require.NotPanics(t, func() {
		d.setError(io.EOF)
	})

	require.NotEmpty(t, d.ErrorMsg())
	require.Equal(t, State(Error), d.GetState())
	assert.Contains(t, d.ErrorMsg(), "EOF")
}
