// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mse_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	tmse "github.com/anacrolix/torrent/mse"

	"tyr/internal/mse"

	"tyr/internal/metainfo"
)

func BenchmarkMSE(b *testing.B) {
	b.ReportAllocs()
	b.StopTimer()

	hash := metainfo.Hash{}
	var data = []byte("hello world\n")

	var handleServer = func(conn net.Conn) {
		defer conn.Close()

		rw, err := mse.NewAccept(conn, []metainfo.Hash{hash}, func(method mse.CryptoMethod) mse.CryptoMethod {
			return mse.CryptoMethod(tmse.DefaultCryptoSelector(tmse.CryptoMethod(method)))
		})
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

		rw, err := mse.NewConnection(hash[:], conn)
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
	for i := 0; i < b.N; i++ {
		server, client := net.Pipe()
		go handleServer(server)
		handleClient(client)
	}
}

func TestDial(t *testing.T) {
	p := 8006
	l := lo.Must(net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)))
	defer l.Close()

	hash := metainfo.Hash{}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}

			go func() {
				defer conn.Close()

				rw, _, err := tmse.ReceiveHandshake(conn, func(callback func(skey []byte) (more bool)) {
					callback(hash[:])
				}, tmse.DefaultCryptoSelector)
				if err != nil {
					return
				}

				echoConnection(rw)
			}()
		}
	}()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	require.NoError(t, err)
	defer conn.Close()

	rw, err := mse.NewConnection(hash[:], conn)
	require.NoError(t, err)

	data := []byte("hello world\n")

	n, err := rw.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)

	var b = make([]byte, len(data))
	_, err = io.ReadFull(rw, b)
	require.NoError(t, err)
	require.Equal(t, data, b)
}

func TestMseAccept(t *testing.T) {
	p := 8005
	l := lo.Must(net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)))
	defer l.Close()

	hash := metainfo.Hash{}

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}

			go func() {
				defer conn.Close()

				rw, err := mse.NewAccept(conn, []metainfo.Hash{hash}, mse.DefaultCryptoSelector)
				if err != nil {
					return
				}

				echoConnection(rw)
			}()
		}
	}()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	require.NoError(t, err)
	defer conn.Close()

	rw, _, err := tmse.InitiateHandshake(conn, hash[:], nil, tmse.CryptoMethodRC4)
	require.NoError(t, err)

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
