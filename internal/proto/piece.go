// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
	"slices"

	"github.com/docker/go-units"

	"tyr/internal/pkg/as"
	"tyr/internal/pkg/gsync"
)

type ChunkResponse struct {
	// len(Data) should match request
	Data       []byte
	Begin      uint32
	PieceIndex uint32
}

func (c *ChunkResponse) Less(b *ChunkResponse) bool {
	if c.PieceIndex == b.PieceIndex {
		return c.Begin < b.Begin
	}

	return c.PieceIndex < b.PieceIndex
}

func (c ChunkResponse) Request() ChunkRequest {
	return ChunkRequest{
		PieceIndex: c.PieceIndex,
		Begin:      c.Begin,
		Length:     as.Uint32(len(c.Data)),
	}
}

func SendPiece(conn io.Writer, r *ChunkResponse) error {
	buf := pool.Get()
	defer pool.Put(buf)

	buf.B = binary.BigEndian.AppendUint32(buf.B, uint32(len(r.Data)+sizeByte+sizeUint32*2))
	buf.B = append(buf.B, byte(Piece))
	buf.B = binary.BigEndian.AppendUint32(buf.B, r.PieceIndex)
	buf.B = binary.BigEndian.AppendUint32(buf.B, r.Begin)

	_, err := conn.Write(buf.B)
	if err != nil {
		return err
	}

	_, err = conn.Write(r.Data)
	return err
}

var PiecePool = gsync.NewPool(func() *ChunkResponse {
	return &ChunkResponse{
		Data: make([]byte, 0, units.KiB*16),
	}
})

func ReadPiecePayload(conn io.Reader, size uint32) (*ChunkResponse, error) {
	var b [sizeUint32 * 2]byte

	_, err := io.ReadFull(conn, b[:])
	if err != nil {
		return nil, err
	}

	var payload = PiecePool.Get()

	payload.PieceIndex = binary.BigEndian.Uint32(b[:])
	payload.Begin = binary.BigEndian.Uint32(b[sizeUint32 : sizeUint32*2])
	payload.Data = slices.Grow(payload.Data[:0], int(size-sizeUint32*2))[:size-sizeUint32*2]

	_, err = io.ReadFull(conn, payload.Data)

	return payload, err
}
