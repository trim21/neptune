// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mse_test

import (
	"bytes"
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
	keyBytes := [][]byte{hash[:]}
	var data = []byte("hello world\n")

	var handleServer = func(conn net.Conn) {
		defer conn.Close()

		rw, _, err := mse.NewAccept(conn, keyBytes, mse.DefaultCryptoSelector)
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

func testMseRoundTrip(t *testing.T, selector mse.CryptoSelector, expectedMethod mse.CryptoMethod) {
	t.Helper()

	hash := metainfo.Hash{}
	keyBytes := [][]byte{hash[:]}
	data := []byte("hello world\n")

	server, client := net.Pipe()

	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		rw, _, err := mse.NewAccept(server, keyBytes, selector)
		if err != nil {
			errCh <- err
			return
		}

		bb := make([]byte, len(data))
		_, err = io.ReadFull(rw, bb)
		if err != nil {
			errCh <- err
			return
		}

		_, err = rw.Write(bb)
		errCh <- err
	}()

	rw, method, err := mse.NewConnection(hash[:], client, mse.AllSupportedCrypto)
	require.NoError(t, err)
	assert.Equal(t, expectedMethod, method)

	n, err := rw.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	b := make([]byte, len(data))
	_, err = io.ReadFull(rw, b)
	require.NoError(t, err)
	require.Equal(t, data, b)

	client.Close()
	require.NoError(t, <-errCh)
}

func TestRoundTrip(t *testing.T) {
	testMseRoundTrip(t, mse.DefaultCryptoSelector, mse.CryptoMethodPlaintext)
}

func TestForceCrypto(t *testing.T) {
	testMseRoundTrip(t, mse.ForceCrypto, mse.CryptoMethodRC4)
}
