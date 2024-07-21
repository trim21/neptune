// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gsync

import (
	"sync/atomic" //nolint:depguard
)

type AtomicUint[T interface{ ~uint8 | ~uint16 }] struct {
	v atomic.Uint32
}

func (u *AtomicUint[T]) Load() T {
	return T(u.v.Load())
}

func (u *AtomicUint[T]) Store(val T) {
	u.v.Store(uint32(val))
}
