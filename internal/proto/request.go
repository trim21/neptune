// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

type ChunkRequest struct {
	PieceIndex uint32
	Begin      uint32
	Length     uint32
}

func SendRequest(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Request, request)
}

func SendCancel(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Cancel, request)
}

func SendReject(conn io.Writer, request ChunkRequest) error {
	return sendRequestPayload(conn, Reject, request)
}

func sendRequestPayload(conn io.Writer, id Message, request ChunkRequest) error {
	buf := pool.Get()
	defer pool.Put(buf)

	buf.B = binary.BigEndian.AppendUint32(buf.B, sizeByte+sizeUint32*3)
	buf.B = append(buf.B, byte(id))
	buf.B = binary.BigEndian.AppendUint32(buf.B, request.PieceIndex)
	buf.B = binary.BigEndian.AppendUint32(buf.B, request.Begin)
	buf.B = binary.BigEndian.AppendUint32(buf.B, request.Length)

	_, err := conn.Write(buf.B)
	return err
}

func ReadRequestPayload(conn io.Reader) (payload ChunkRequest, err error) {
	var b [sizeUint32 * 3]byte

	_, err = io.ReadFull(conn, b[:])
	if err != nil {
		return
	}

	payload.PieceIndex = binary.BigEndian.Uint32(b[:])
	payload.Begin = binary.BigEndian.Uint32(b[sizeUint32:])
	payload.Length = binary.BigEndian.Uint32(b[sizeUint32*2:])

	return
}
