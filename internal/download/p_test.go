// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

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
	wg.Go(func() {
		_, _ = io.Copy(io.Discard, c2)
	})

	p := &peerImpl{
		Conn:            c1,
		w:               bufio.NewWriterSize(c1, 64*1024),
		log:             zerolog.New(io.Discard),
		lastSend:        *atomic.NewInt64(time.Now().Unix()),
		pieceUploadRate: flowrate.New(time.Second, time.Second),
		peerRequests:    xsync.NewMap[proto.ChunkRequest, empty.Empty](),
	}

	data := []byte{1, 2, 3, 4, 5, 6, 7}
	req := proto.ChunkRequest{PieceIndex: 1, Begin: 0, Length: uint32(len(data))}
	p.peerRequests.Store(req, empty.Empty{})

	ok := p.Response(&proto.ChunkResponse{PieceIndex: 1, Begin: 0, Data: data})
	require.True(t, ok)

	_ = c1.Close()
	_ = c2.Close()
	wg.Wait()

	require.Equal(t, int64(len(data)), p.pieceUploadRate.Done())
}

func TestPeerResponseNoRequestDoesNotWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	p := &peerImpl{
		Conn:            c1,
		w:               bufio.NewWriterSize(c1, 64*1024),
		log:             zerolog.New(io.Discard),
		lastSend:        *atomic.NewInt64(time.Now().Unix()),
		pieceUploadRate: flowrate.New(time.Second, time.Second),
		peerRequests:    xsync.NewMap[proto.ChunkRequest, empty.Empty](),
	}

	data := []byte{9, 9, 9, 9}
	ok := p.Response(&proto.ChunkResponse{PieceIndex: 1, Begin: 0, Data: data})
	require.False(t, ok)
	require.Equal(t, int64(0), p.pieceUploadRate.Done())

	_ = c2.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := c2.Read(buf)
	require.Error(t, err)

	ne, okNet := err.(net.Error)
	require.True(t, okNet)
	require.True(t, ne.Timeout())
}

func TestPieceBlockQueueWrapAround(t *testing.T) {
	var q pieceBlockQueue

	for i := range maxRequestQueue {
		require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: uint32(i), BlockIndex: uint32(i)}}))
	}
	require.False(t, q.Push(BlockClaim{}))

	for i := range maxRequestQueue / 2 {
		block, ok := q.Front()
		require.True(t, ok)
		require.Equal(t, uint32(i), block.Block.PieceIndex)
		q.Pop()
	}

	for i := range maxRequestQueue / 2 {
		value := maxRequestQueue + i
		require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: uint32(value), BlockIndex: uint32(value)}}))
	}

	for i := maxRequestQueue / 2; i < maxRequestQueue+maxRequestQueue/2; i++ {
		block, ok := q.Front()
		require.True(t, ok)
		require.Equal(t, uint32(i), block.Block.PieceIndex)
		q.Pop()
	}

	require.Zero(t, q.Len())
	_, ok := q.Front()
	require.False(t, ok)
}

func TestPieceBlockQueueRemoveWrappedEntry(t *testing.T) {
	var q pieceBlockQueue

	for i := range maxRequestQueue {
		require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: uint32(i), BlockIndex: uint32(i)}}))
	}
	for range maxRequestQueue - 2 {
		q.Pop()
	}
	require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: 2000, BlockIndex: 2000}}))
	require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: 2001, BlockIndex: 2001}}))

	require.True(t, q.Remove(1999, 1999))
	require.False(t, q.Remove(42, 42))

	var got []uint32
	q.Range(func(claim BlockClaim) bool {
		got = append(got, claim.Block.PieceIndex)
		return true
	})
	require.Equal(t, []uint32{1998, 2000, 2001}, got)

	q.Clear()
	require.Zero(t, q.Len())
	require.True(t, q.Push(BlockClaim{Block: PieceBlock{PieceIndex: 1, BlockIndex: 1}}))
	block, ok := q.Front()
	require.True(t, ok)
	require.Equal(t, uint32(1), block.Block.PieceIndex)
}
