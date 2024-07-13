// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mempool

import (
	"github.com/valyala/bytebufferpool"
)

type Buffer = bytebufferpool.ByteBuffer

func New() bytebufferpool.Pool {
	return bytebufferpool.Pool{}
}

func GetWithCap(size int) *Buffer {
	buf := bytebufferpool.Get()

	if cap(buf.B) < size {
		buf.B = make([]byte, size)
	} else {
		buf.B = buf.B[:size]
	}

	return buf
}

func Get() *Buffer {
	return bytebufferpool.Get()
}

func Put(b *Buffer) {
	bytebufferpool.Put(b)
}
