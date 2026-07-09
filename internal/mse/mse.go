// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mse

import (
	"io"
	"net"

	"neptune/internal/metainfo"
	"neptune/internal/mse/mse"
)

type CryptoMethod = mse.CryptoMethod
type CryptoSelector = mse.CryptoSelector
type SecretKeyIter = mse.SecretKeyIter

const (
	CryptoMethodPlaintext = mse.CryptoMethodPlaintext
	CryptoMethodRC4       = mse.CryptoMethodRC4
	AllSupportedCrypto    = mse.AllSupportedCrypto
)

func DefaultCryptoSelector(provided CryptoMethod) CryptoMethod {
	// We prefer plaintext for performance reasons.
	if provided&mse.CryptoMethodPlaintext != 0 {
		return mse.CryptoMethodPlaintext
	}
	return mse.CryptoMethodRC4
}

func ForceCrypto(provided mse.CryptoMethod) mse.CryptoMethod {
	return mse.CryptoMethodRC4
}

func PreferCrypto(provided mse.CryptoMethod) mse.CryptoMethod {
	if provided&mse.CryptoMethodRC4 != 0 {
		return mse.CryptoMethodRC4
	}
	return mse.CryptoMethodPlaintext
}

func keyMatcher(keys []metainfo.Hash) func(f func([]byte) bool) {
	return func(f func([]byte) bool) {
		for _, ih := range keys {
			if !f(ih[:]) {
				break
			}
		}
	}
}

func NewAccept(conn net.Conn, keys []metainfo.Hash, selector mse.CryptoSelector) (net.Conn, CryptoMethod, error) {
	rw, method, err := mse.ReceiveHandshake(conn, keyMatcher(keys), selector)
	if err != nil {
		_ = conn.Close()
		return nil, 0, err
	}

	return wrappedConn{rw: rw, Conn: conn}, method, err
}

func NewConnection(infoHash []byte, conn net.Conn, cryptoProvides CryptoMethod) (net.Conn, CryptoMethod, error) {
	ret, method, err := mse.InitiateHandshake(conn, infoHash, nil, cryptoProvides)
	if err != nil {
		return nil, 0, err
	}

	return wrappedConn{rw: ret, Conn: conn}, method, nil
}

var _ io.ReadWriteCloser = wrappedConn{}

type wrappedConn struct {
	net.Conn
	rw io.ReadWriter
}

func (c wrappedConn) Read(b []byte) (n int, err error) {
	return c.rw.Read(b)
}

func (c wrappedConn) Write(b []byte) (n int, err error) {
	return c.rw.Write(b)
}
