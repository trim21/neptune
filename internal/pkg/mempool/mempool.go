// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mempool

import (
	"github.com/valyala/bytebufferpool"
)

func GetWithCap(size int) *bytebufferpool.ByteBuffer {
	buf := bytebufferpool.Get()

	if cap(buf.B) < size {
		buf.B = make([]byte, 0, size)
	}

	return buf
}

func Get() *bytebufferpool.ByteBuffer {
	return bytebufferpool.Get()
}

func Put(b *bytebufferpool.ByteBuffer) {
	bytebufferpool.Put(b)
}
