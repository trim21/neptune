// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bep40

import (
	"tyr/internal/pkg/crc32c"
)

func SimplePriority(key []byte, addr []byte) uint32 {
	var bs = make([]byte, len(key)+len(addr))

	copy(bs, key)
	copy(bs[len(key):], addr)

	return crc32c.Sum(bs)
}
