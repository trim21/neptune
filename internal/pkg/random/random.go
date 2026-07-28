// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package random

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/unsafe"
)

var p = gsync.NewPool(func() *bufio.Reader {
	return bufio.NewReader(rand.Reader)
})

const base64UrlSafeChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-_"

// URLSafeStr generate a cryptographically secure url safe string in given length.
// result is not a valid base64 string or base64url string
// entropy = 64^size.
func URLSafeStr(size int) string {
	r := Bytes(size)

	for i, rb := range r {
		// len(base64UrlSafeChars) % 64 == 0 so it's not bias
		r[i] = base64UrlSafeChars[rb%64]
	}

	return unsafe.Str(r)
}

const printable = " !\"#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\\]^_`abcdefghijklmnopqrstuvwxyz{|}~"
const printableCharsLength = byte(len(printable))
const printableMaxByte = byte(255 - (256 % len(printable)))

func PrintableBytes(size int) []byte {
	reader := p.Get()
	defer p.Put(reader)

	b := make([]byte, size)
	r := make([]byte, size+size/2) // storage for random bytes.
	i := 0

	for {
		_, err := io.ReadFull(reader, r)
		if err != nil {
			panic("unexpected error happened when reading from bufio.NewReader(crypto/rand.Reader)")
		}
		for _, rb := range r {
			if rb > printableMaxByte { // Skip this number to avoid modulo bias.
				continue
			}
			b[i] = printable[rb%printableCharsLength]
			i++
			if i == size {
				return b
			}
		}
	}
}

// DeriveKey deterministically derives a URL-safe key string from a seed and infoHash
// using HMAC-SHA256, producing a string of the given length.
// The output uses the same alphabet as URLSafeStr (64 URL-safe characters).
func DeriveKey(seed, infoHash string, size int) string {
	mac := hmac.New(sha256.New, unsafe.Bytes(seed))
	mac.Write(unsafe.Bytes(infoHash))
	sum := mac.Sum(nil)

	r := make([]byte, size)
	for i := range r {
		r[i] = base64UrlSafeChars[sum[i%len(sum)]%64]
	}
	return unsafe.Str(r)
}

// Bytes generate a cryptographically secure random bytes.
// Will panic if it can't read from 'crypto/rand'.
// entropy = 256^size.
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

// Uint64 returns a cryptographically secure random uint64.
func Uint64() uint64 {
	reader := p.Get()
	defer p.Put(reader)

	var buf [8]byte
	_, err := io.ReadFull(reader, buf[:])
	if err != nil {
		panic(fmt.Sprintf("unexpected error happened when reading from bufio.NewReader(crypto/rand.Reader) %+v", err))
	}
	return binary.BigEndian.Uint64(buf[:])
}

// Int64N returns, as an int64, a non-negative pseudo-random number in the
// half-open interval [0,n). It panics if n <= 0.
func Int64N(n int64) int64 {
	if n <= 0 {
		panic("invalid argument to Int64N")
	}
	if n&(n-1) == 0 { // n is a power of two
		return int64(Uint64() & uint64(n-1))
	}
	// Avoid modulo bias with rejection sampling.
	threshold := int64((1<<63 - 1) - (1<<63-1)%uint64(n) - 1)
	v := int64(Uint64() >> 1) // positive int63
	for v > threshold {
		v = int64(Uint64() >> 1)
	}
	return v % n
}
