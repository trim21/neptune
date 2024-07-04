// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bep40

import (
	"encoding/binary"
)

func portBytes(a, b uint16) []byte {
	var ret [4]byte
	if a < b {
		binary.BigEndian.PutUint16(ret[0:2], a)
		binary.BigEndian.PutUint16(ret[2:4], b)
	} else {
		binary.BigEndian.PutUint16(ret[0:2], b)
		binary.BigEndian.PutUint16(ret[2:4], a)
	}
	return ret[:]
}
