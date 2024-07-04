// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package random

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"

	"tyr/internal/pkg/pool"
	"tyr/internal/pkg/unsafe"
)

var p = pool.New(func() *bufio.Reader {
	return bufio.NewReader(rand.Reader)
})

const base64UrlSafeChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_"

// UrlSafeStr generate a cryptographically secure url safe string in given length.
// result is not a valid base64 string or base64url string
// entropy = 64^size
func UrlSafeStr(size int) string {
	r := Bytes(size)

	for i, rb := range r {
		// len(base64UrlSafeChars) % 64 == 0 so it's not biaed
		r[i] = base64UrlSafeChars[rb%64]
	}

	return unsafe.Str(r)
}

// Bytes generate a cryptographically secure random bytes.
// Will panic if it can't read from 'crypto/rand'.
// entropy = 256^size
func Bytes(size int) []byte {
	reader := p.Get()
	defer p.Put(reader)

	r := make([]byte, size)
	_, err := io.ReadFull(reader, r)
	if err != nil {
		panic(fmt.Sprintf("unexpected error happened when reading from bufio.NewReader(crypto/rand.Reader) %+v", err))
	}

	return r
}
