// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package unsafe

import (
	"unsafe"
)

func Bytes(s string) []byte {
	d := unsafe.StringData(s)
	return unsafe.Slice(d, len(s))
}

func Str(b []byte) string {
	d := unsafe.SliceData(b)
	return unsafe.String(d, len(b))
}
