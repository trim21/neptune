// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"runtime"
	"slices"
	"sync"

	"github.com/kelindar/bitmap"
)

func init() {
	if binary.NativeEndian.Uint16([]byte{0x12, 0x34}) != uint16(0x3412) {
		panic(fmt.Sprintf("current implement of bitmap asscume running on little endian cpu, but %s is a big endian", runtime.GOARCH))
	}
}

func New(size uint32) *Bitmap {
	return &Bitmap{
		size: size,
		bm:   make(bitmap.Bitmap, (size+7)/8),
	}
}

func FromBitfields(bitfield []byte, size uint32) *Bitmap {
	var b = make(bitmap.Bitmap, (size+7)/8)

	nn := make([]byte, size+8)

	copy(nn, bitfield)

	for i := uint32(0); i < (size+7)/8; i++ {
		b[i] = bits.Reverse64(binary.BigEndian.Uint64(nn[i*8 : i*8+8]))
	}

	return &Bitmap{
		size: size,
		bm:   b,
	}
}

// Bitmap is thread-safe bitmap wrapper
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

// Fill bitmap [0, b.size)
func (b *Bitmap) Fill() {
	b.m.Lock()
	if b.size >= 63 {
		b.bm.Grow((b.size) / 64 * 64)
		b.bm.Ones()
	}

	for i := b.size / 64 * 64; i < b.size; i++ {
		b.bm.Set(i)
	}

	b.m.Unlock()
}

func (b *Bitmap) Count() uint32 {
	b.m.RLock()
	v := uint32(b.bm.CountTo(b.size))
	b.m.RUnlock()
	return v
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

func (b *Bitmap) CompressedBytes() []byte {
	b.m.RLock()
	defer b.m.RUnlock()
	return slices.Clone(b.bm.ToBytes())
}

func (b *Bitmap) Range(fn func(uint32)) {
	b.m.RLock()
	defer b.m.RUnlock()
	b.bm.Range(fn)
}

// Bitfield return bytes as bittorrent protocol
func (b *Bitmap) Bitfield() []byte {
	b.m.RLock()
	defer b.m.RUnlock()

	bytes := slices.Clone(b.bm.ToBytes())
	for i, item := range bytes {
		bytes[i] = bits.Reverse8(item)
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

func (b *Bitmap) String() string {
	b.m.RLock()
	defer b.m.RUnlock()
	var s []uint32

	b.bm.Range(func(x uint32) {
		s = append(s, x)
	})

	return fmt.Sprintf("*Bitmap{size: %v, bm: %v}", b.size, s)
}
