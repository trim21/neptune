// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"sync"

	"github.com/kelindar/bitmap"
)

func New(size uint32) *Bitmap {
	return &Bitmap{
		size: size,
		bm:   make(bitmap.Bitmap, (size+63)/64),
	}
}

func FromBitfields(bitfield []byte, size uint32) *Bitmap {
	var b = make(bitmap.Bitmap, (size+63)/64)

	nn := make([]byte, size+8)

	copy(nn, bitfield)

	for i := range (size + 63) / 64 {
		// github.com/kelindar/bitmap impl bitmap as little endian in all platform.
		// for example, []uint64{0x00000001} is a bitmap [true, false, ...].
		// so we need to reverse its bits here.
		b[i] = bits.Reverse64(binary.BigEndian.Uint64(nn[i*8 : i*8+8]))
	}

	return &Bitmap{
		size: size,
		bm:   b,
	}
}

// Bitmap is thread-safe bitmap wrapper.
type Bitmap struct {
	bm   bitmap.Bitmap
	m    sync.RWMutex
	size uint32
}

func (b *Bitmap) Clear() {
	b.m.Lock()
	b.bm.Clear()
	b.m.Unlock()
}

// Fill bitmap [0, b.size).
func (b *Bitmap) Fill() {
	b.m.Lock()

	blkAt := b.size >> 6

	bm := make(bitmap.Bitmap, blkAt+1)
	for i := range bm {
		bm[i] = math.MaxUint64
	}

	bm[blkAt] = math.MaxUint64 >> (64 - b.size%64)

	b.bm = bm

	b.m.Unlock()
}

func (b *Bitmap) Count() uint32 {
	b.m.RLock()
	v := uint32(b.bm.CountTo(b.size))
	b.m.RUnlock()
	return v
}

func (b *Bitmap) SetX(i uint32) bool {
	b.m.Lock()
	defer b.m.Unlock()
	if b.bm.Contains(i) {
		return false
	}

	b.bm.Set(i)
	return true
}

func (b *Bitmap) Set(i uint32) {
	b.m.Lock()
	b.bm.Set(i)
	b.m.Unlock()
}

func (b *Bitmap) Unset(i uint32) {
	b.m.Lock()
	b.bm.Remove(i)
	b.m.Unlock()
}

func (b *Bitmap) XOR(bm *Bitmap) {
	bm.m.RLock()
	b.m.Lock()
	b.bm.Xor(bm.bm)
	b.m.Unlock()
	bm.m.RUnlock()
}

func (b *Bitmap) Contains(i uint32) bool {
	b.m.RLock()
	v := b.bm.Contains(i)
	b.m.RUnlock()
	return v
}

func (b *Bitmap) Range(fn func(uint32)) {
	b.m.RLock()
	defer b.m.RUnlock()
	b.bm.Range(fn)
}

// Bitfield return bytes as bittorrent protocol.
func (b *Bitmap) Bitfield() []byte {
	b.m.RLock()
	defer b.m.RUnlock()

	// avoid alloc
	var bytes = make([]byte, 0, len(b.bm)*8)

	for _, u := range b.bm {
		// see [FromBitfields] why we do this
		bytes = binary.BigEndian.AppendUint64(bytes, bits.Reverse64(u))
	}

	return bytes[:(b.size+7)/8]
}

func (b *Bitmap) OR(bm *Bitmap) {
	b.m.Lock()
	defer b.m.Unlock()

	bm.m.RLock()
	defer bm.m.RUnlock()

	b.bm.Or(bm.bm)
}

func (b *Bitmap) Clone() *Bitmap {
	b.m.RLock()
	defer b.m.RUnlock()

	return &Bitmap{
		size: b.size,
		bm:   slices.Clone(b.bm),
	}
}

func (b *Bitmap) WithAnd(bm *Bitmap) *Bitmap {
	m := b.Clone()

	bm.m.RLock()
	m.bm.And(bm.bm)
	bm.m.RUnlock()

	return m
}

func (b *Bitmap) AndNot(bm *Bitmap) {
	bm.m.RLock()
	defer bm.m.RUnlock()

	b.m.Lock()
	defer b.m.Unlock()

	b.bm.AndNot(bm.bm)
}

func (b *Bitmap) WithAndNot(bm *Bitmap) *Bitmap {
	m := b.Clone()

	bm.m.RLock()
	m.bm.AndNot(bm.bm)
	bm.m.RUnlock()

	return m
}

func (b *Bitmap) WithOr(bm *Bitmap) *Bitmap {
	m := b.Clone()

	bm.m.RLock()
	m.bm.Or(bm.bm)
	bm.m.RUnlock()

	return m
}

func (b *Bitmap) ToArray() []uint32 {
	var s = make([]uint32, 0, 10)

	b.bm.Range(func(x uint32) {
		s = append(s, x)
	})

	return s
}

func (b *Bitmap) String() string {
	b.m.RLock()
	defer b.m.RUnlock()
	var s []uint32

	b.bm.Range(func(x uint32) {
		s = append(s, x)
	})

	return fmt.Sprintf("*Bitmap{size: %v, bm: %v}", b.size, s)
}
