// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/mse"
	"neptune/internal/piece_store"
	"neptune/internal/proto"
)

var testOutgoingPeerID [20]byte

// TestConnectPeerFallbackToPlaintext verifies that in prefer mode a failed
// MSE handshake falls back to plaintext on a fresh TCP connection. The first
// connection (polluted by MSE handshake bytes) must be closed, and the second
// connection must carry a clean plaintext BitTorrent handshake.
func TestConnectPeerFallbackToPlaintext(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr).AddrPort()

	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.session.MSEEnabled = true
	d.session.MSEForce = false
	d.session.MSEPreferredCrypto = mse.AllSupportedCrypto

	serverErr := make(chan error, 1)
	go func() {
		// First connection: a non-MSE peer. Consume the MSE handshake bytes
		// and close, so the client's MSE handshake fails with EOF.
		c1, aErr := ln.Accept()
		if aErr != nil {
			serverErr <- aErr
			return
		}
		// The initiator sends a 96-byte Y first; read it, then close so the
		// client's read of Y' fails immediately instead of hanging.
		_, _ = io.ReadFull(c1, make([]byte, 96))
		_ = c1.Close()

		// Second connection: plaintext peer. Complete a BitTorrent handshake.
		c2, aErr := ln.Accept()
		if aErr != nil {
			serverErr <- aErr
			return
		}
		defer c2.Close()

		h, hErr := proto.ReadHandshake(c2)
		if hErr != nil {
			serverErr <- hErr
			return
		}
		serverErr <- proto.SendHandshake(c2, h.InfoHash, testOutgoingPeerID, false)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pp := &persistentPeer{addrPort: addr}
	pc, err := d.connectPeer(ctx, pp)
	require.NoError(t, err)
	require.False(t, pc.encrypted, "fallback connection must be plaintext")
	defer pc.conn.Close()

	// Plaintext handshake on the fresh connection (normally done by the peer).
	require.NoError(t, proto.SendHandshake(pc.conn, d.info.Hash, testOutgoingPeerID, d.private))
	h, err := proto.ReadHandshake(pc.conn)
	require.NoError(t, err)
	require.Equal(t, d.info.Hash, h.InfoHash)

	select {
	case err := <-serverErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not complete plaintext handshake")
	}
}

// TestConnectPeerMSEForceNoFallback verifies that in force mode a failed MSE
// handshake fails the connection outright: no plaintext fallback is attempted
// and the connection slot is released.
func TestConnectPeerMSEForceNoFallback(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr).AddrPort()

	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.session.MSEEnabled = true
	d.session.MSEForce = true
	d.session.MSEPreferredCrypto = mse.AllSupportedCrypto

	accepted := make(chan struct{}, 1)
	go func() {
		c, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		accepted <- struct{}{}
		_, _ = io.ReadFull(c, make([]byte, 96))
		_ = c.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pp := &persistentPeer{addrPort: addr}
	pc, err := d.connectPeer(ctx, pp)
	require.Error(t, err, "force mode must fail on MSE handshake error")
	require.Nil(t, pc.conn)

	<-accepted

	// No fallback: no second connection should arrive.
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(200 * time.Millisecond))
	_, err = ln.Accept()
	require.Error(t, err, "force mode must not attempt a plaintext fallback connection")

	// The failed attempt must have released the connection slot.
	require.True(t, d.session.ConnSem.TryAcquire(200), "failed connect must release the connection slot")
}

// TestConnectPeerReturnsErrConnectionLimit verifies that a full global
// connection slot fails fast with errConnectionLimit, without dialing or
// establishing any connection. tryDial skips the peer's failcount for this
// error because it is our own capacity limit, not a peer failure.
func TestConnectPeerReturnsErrConnectionLimit(t *testing.T) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr).AddrPort()

	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	// Occupy the full connection semaphore.
	require.True(t, d.session.ConnSem.TryAcquire(200))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	pp := &persistentPeer{addrPort: addr}
	pc, err := d.connectPeer(ctx, pp)
	require.ErrorIs(t, err, errConnectionLimit)
	require.Nil(t, pc.conn)

	// No connection should have been dialed.
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond))
	_, err = ln.Accept()
	require.Error(t, err, "no connection should be dialed when the slot is full")
}
