// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bep40

import (
	"bytes"
	"net/netip"

	"tyr/internal/pkg/crc32c"
)

func maskV4(s [4]byte, size int) [4]byte {
	var masked = s

	for i := size; i < 4; i++ {
		masked[i] = s[i] & 0x55
	}

	return masked
}

func priorityBytes4(a, b netip.AddrPort) []byte {
	if a.Addr() == b.Addr() {
		return portBytes(a.Port(), b.Port())
	}

	if !a.Addr().Is4() || !b.Addr().Is4() {
		panic("not v4 addr")
	}

	ad := a.Addr().As4()
	bd := b.Addr().As4()

	var size = 2
	for i := 4; i >= 2; i-- {
		if bytes.Equal(ad[:i], bd[:i]) {
			size = i + 1
			break
		}
	}

	var ma = maskV4(ad, size)
	var mb = maskV4(bd, size)

	if bytes.Compare(ma[:], mb[:]) < 0 {
		return append(ma[:], mb[:]...)
	}

	return append(mb[:], ma[:]...)
}

func Priority4(client, peer netip.AddrPort) uint32 {
	bs := priorityBytes4(client, peer)

	return crc32c.Sum(bs)
}
