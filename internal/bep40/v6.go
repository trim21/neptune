// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bep40

import (
	"bytes"
	"net/netip"

	"tyr/internal/pkg/crc32c"
)

func maskV6(s [16]byte, size int) [16]byte {
	var masked = s

	for i := size; i < 16; i++ {
		masked[i] = s[i] & 0x55
	}

	return masked
}

func priorityBytes6(a, b netip.AddrPort) []byte {
	if a.Addr() == b.Addr() {
		return portBytes(a.Port(), b.Port())
	}

	if !a.Addr().Is6() || !b.Addr().Is6() {
		panic("not v6 addr")
	}

	ad := a.Addr().As16()
	bd := b.Addr().As16()

	var size = 48 / 8
	for i := 16 - 2; i >= 6; i = i - 2 {
		if bytes.Equal(ad[:i], bd[:i]) {
			size = i + 2
			break
		}
	}

	ma := maskV6(ad, size)
	mb := maskV6(bd, size)

	if bytes.Compare(ma[:], mb[:]) < 0 {
		return append(ma[:], mb[:]...)
	}

	return append(mb[:], ma[:]...)
}

func Priority6(client, peer netip.AddrPort) uint32 {
	bs := priorityBytes6(client, peer)

	return crc32c.Sum(bs)
}
