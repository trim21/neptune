// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bufio"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/proto"
)

func TestPeerID(t *testing.T) {
	require.Len(t, peerIDPrefix, 8)
}

func TestPeerResponseSendsOnceAndCountsOnce(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, c2)
	}()

	p := &Peer{
		Conn:         c1,
		w:            bufio.NewWriterSize(c1, 64*1024),
		log:          zerolog.New(io.Discard),
		lastSend:     *atomic.NewTime(time.Now()),
		ioOut:        flowrate.New(time.Second, time.Second),
		peerRequests: xsync.NewMap[proto.ChunkRequest, empty.Empty](),
	}

	data := []byte{1, 2, 3, 4, 5, 6, 7}
	req := proto.ChunkRequest{PieceIndex: 1, Begin: 0, Length: uint32(len(data))}
	p.peerRequests.Store(req, empty.Empty{})

	ok := p.Response(&proto.ChunkResponse{PieceIndex: 1, Begin: 0, Data: data})
	require.True(t, ok)

	_ = c1.Close()
	_ = c2.Close()
	wg.Wait()

	require.Equal(t, int64(len(data)), p.ioOut.Done())
}

func TestPeerResponseNoRequestDoesNotWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	p := &Peer{
		Conn:         c1,
		w:            bufio.NewWriterSize(c1, 64*1024),
		log:          zerolog.New(io.Discard),
		lastSend:     *atomic.NewTime(time.Now()),
		ioOut:        flowrate.New(time.Second, time.Second),
		peerRequests: xsync.NewMap[proto.ChunkRequest, empty.Empty](),
	}

	data := []byte{9, 9, 9, 9}
	ok := p.Response(&proto.ChunkResponse{PieceIndex: 1, Begin: 0, Data: data})
	require.False(t, ok)
	require.Equal(t, int64(0), p.ioOut.Done())

	_ = c2.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := c2.Read(buf)
	require.Error(t, err)

	ne, okNet := err.(net.Error)
	require.True(t, okNet)
	require.True(t, ne.Timeout())
}
