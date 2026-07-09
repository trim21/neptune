// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mse_test

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"neptune/internal/metainfo"
	"neptune/internal/mse"
)

func BenchmarkMSE(b *testing.B) {
	b.ReportAllocs()
	b.StopTimer()

	hash := metainfo.Hash{}
	var data = []byte("hello world\n")

	var handleServer = func(conn net.Conn) {
		defer conn.Close()

		rw, _, err := mse.NewAccept(conn, []metainfo.Hash{hash}, mse.DefaultCryptoSelector)
		if err != nil {
			return
		}

		bb := make([]byte, len(data))
		_, err = io.ReadFull(rw, bb)
		if err != nil {
			return
		}

		rw.Write(bb)
	}

	var handleClient = func(conn net.Conn) {
		defer conn.Close()

		rw, _, err := mse.NewConnection(hash[:], conn, mse.AllSupportedCrypto)
		if err != nil {
			panic(err)
		}

		n, err := rw.Write(data)
		if err != nil {
			panic(err)
		}

		var buf = make([]byte, n)

		_, err = rw.Read(buf)
		if err != nil {
			panic(err)
		}

		if !bytes.Equal(buf, data) {
			panic("bad response")
		}
	}

	b.StartTimer()
	for range b.N {
		server, client := net.Pipe()
		go handleServer(server)
		handleClient(client)
	}
}

func TestRoundTrip(t *testing.T) {
	lc := &net.ListenConfig{}
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()

	hash := metainfo.Hash{}

	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer conn.Close()

				rw, _, acceptErr := mse.NewAccept(conn, []metainfo.Hash{hash}, mse.DefaultCryptoSelector)
				if acceptErr != nil {
					return
				}

				echoConnection(rw)
			}()
		}
	}()

	d := &net.Dialer{}
	conn, err := d.DialContext(context.Background(), "tcp", l.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	rw, method, err := mse.NewConnection(hash[:], conn, mse.AllSupportedCrypto)
	require.NoError(t, err)
	assert.Equal(t, mse.CryptoMethodPlaintext, method)

	data := []byte("hello world\n")

	n, err := rw.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	var b = make([]byte, len(data))
	_, err = io.ReadFull(rw, b)
	require.NoError(t, err)
	require.Equal(t, data, b)
}

func TestForceCrypto(t *testing.T) {
	lc := &net.ListenConfig{}
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()

	hash := metainfo.Hash{}
	serverMethod := make(chan mse.CryptoMethod, 1)

	go func() {
		for {
			conn, acceptErr := l.Accept()
			if acceptErr != nil {
				return
			}

			go func() {
				defer conn.Close()

				rw, method, acceptErr := mse.NewAccept(conn, []metainfo.Hash{hash}, mse.ForceCrypto)
				if acceptErr != nil {
					return
				}

				serverMethod <- method
				echoConnection(rw)
			}()
		}
	}()

	d := &net.Dialer{}
	conn, err := d.DialContext(context.Background(), "tcp", l.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	rw, method, err := mse.NewConnection(hash[:], conn, mse.AllSupportedCrypto)
	require.NoError(t, err)
	assert.Equal(t, mse.CryptoMethodRC4, method)

	sm := <-serverMethod
	assert.Equal(t, mse.CryptoMethodRC4, sm)

	data := []byte("hello world\n")
	n, err := rw.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	var b = make([]byte, len(data))
	_, err = io.ReadFull(rw, b)
	require.NoError(t, err)
	require.Equal(t, data, b)
}

func echoConnection(conn io.ReadWriter) {
	var b = make([]byte, 4)

	for {
		n, err := conn.Read(b)
		if err != nil {
			return
		}

		_, err = conn.Write(b[:n])
		if err != nil {
			return
		}
	}
}

func loMust[T any](t *testing.T, v T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return v
}
