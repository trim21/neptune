// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package rc4 implements RC4 encryption with an optimized amd64 assembly version.
//
// RC4 is cryptographically broken; Neptune uses it only for MSE (Message Stream
// Encryption) obfuscation, not for security.
package rc4

import "strconv"

// A Cipher is an instance of RC4 using a particular key.
// The state layout ([256]uint32 + two uint8) must match the assembly.
type Cipher struct {
	s    [256]uint32
	i, j uint8
}

// KeySizeError indicates an invalid key size.
type KeySizeError int

func (k KeySizeError) Error() string {
	return "rc4: invalid key size " + strconv.Itoa(int(k))
}

// NewCipher creates and returns a new Cipher. The key must be 1-256 bytes.
func NewCipher(key []byte) (*Cipher, error) {
	k := len(key)
	if k < 1 || k > 256 {
		return nil, KeySizeError(k)
	}
	var c Cipher
	for i := range 256 {
		c.s[i] = uint32(i)
	}
	var j uint8
	for i := range 256 {
		j += uint8(c.s[i]) + key[i%k]
		c.s[i], c.s[j] = c.s[j], c.s[i]
	}
	return &c, nil
}

// xorKeyStream is implemented in assembly on supported platforms.
// On other platforms, rc4_generic.go provides the pure Go fallback.

// XORKeyStream sets dst to the result of XORing src with the key stream.
// Dst and src must overlap entirely or not at all.
func (c *Cipher) XORKeyStream(dst, src []byte) {
	if len(src) == 0 {
		return
	}
	if len(dst) < len(src) {
		panic("rc4: output smaller than input")
	}
	xorKeyStream(&dst[0], &src[0], len(src), &c.s, &c.i, &c.j)
}
