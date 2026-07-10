// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"encoding/binary"
	"io"
	"net"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

// dummyRemotePeer is a simple BitTorrent peer on the remote end of a net.Pipe.
// It sends bitfield, waits for Interested, unchokes, then responds to
// Request messages by reading from a store function.
type dummyRemotePeer struct {
	conn    net.Conn
	r       io.Reader
	w       io.Writer
	ctx     context.Context
	store   func(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error)
	info    meta.Info
	readBuf [4]byte
}

// newDummyRemotePeer creates a pipe and returns the remote peer (on the local end)
// and the peer-side connection. Use the connection to create an IncomingPeer.
func newDummyRemotePeer(
	info meta.Info,
	store func(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error),
	ctx context.Context,
) (*dummyRemotePeer, net.Conn) {
	local, remote := net.Pipe()
	return &dummyRemotePeer{conn: local, r: local, w: local, info: info, store: store, ctx: ctx}, remote
}

func (dp *dummyRemotePeer) Close() error { return dp.conn.Close() }

func (dp *dummyRemotePeer) Serve(done <-chan struct{}) error {
	if err := dp.sendBitfield(); err != nil {
		return err
	}
	if err := dp.waitInterested(done); err != nil {
		return err
	}
	if err := proto.SendUnchoke(dp.w); err != nil {
		return err
	}
	return dp.serveRequests(done)
}

func (dp *dummyRemotePeer) sendBitfield() error {
	bm := bm.New(dp.info.NumPieces)
	bm.Fill()
	return proto.SendBitfield(dp.w, bm)
}

func (dp *dummyRemotePeer) waitInterested(done <-chan struct{}) error {
	for {
		select {
		case <-done:
			return io.EOF
		default:
		}
		msgType, _, err := dp.readMessage()
		if err != nil {
			return err
		}
		switch msgType {
		case byte(proto.Interested):
			return nil
		case byte(proto.NotInterested):
			return nil
		}
	}
}

func (dp *dummyRemotePeer) serveRequests(done <-chan struct{}) error {
	for {
		select {
		case <-done:
			return nil
		default:
		}
		msgType, payload, err := dp.readMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msgType != byte(proto.Request) {
			continue
		}
		req := parseRequest(payload)
		data := make([]byte, req.Length)
		if _, err := dp.store(dp.ctx, req.PieceIndex, req.Begin, data); err != nil {
			return err
		}
		if err := proto.SendPiece(dp.w, &proto.ChunkResponse{
			PieceIndex: req.PieceIndex, Begin: req.Begin, Data: data,
		}); err != nil {
			return err
		}
	}
}

func (dp *dummyRemotePeer) readMessage() (byte, []byte, error) {
	if _, err := io.ReadFull(dp.r, dp.readBuf[:]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(dp.readBuf[:])
	if size == 0 {
		return 0, nil, nil
	}
	if size > 1024*1024 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(dp.r, payload); err != nil {
		return 0, nil, err
	}
	return payload[0], payload[1:], nil
}
