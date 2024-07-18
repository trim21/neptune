// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
	"encoding/binary"
	"sync"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/samber/lo"
)

func New(size uint32) *Bitmap {
	return &Bitmap{
		size: size,
		bm:   roaring.New(),
	}
}

func FromBitmap(bm *roaring.Bitmap, size uint32) *Bitmap {
	bm.RemoveRange(uint64(size), uint64(size+64*8))

	return &Bitmap{
		size: size,
		bm:   bm,
	}
}

// Bitmap is thread-safe bitmap wrapper
type Bitmap struct {
	bm   *roaring.Bitmap
	m    sync.RWMutex
	size uint32
}

func (b *Bitmap) Clear() {
	b.m.Lock()
	b.bm.Clear()
	b.m.Unlock()
}

func (b *Bitmap) Fill() {
	b.m.Lock()
	b.bm.AddRange(0, uint64(b.size))
	b.m.Unlock()
}

func (b *Bitmap) Count() uint32 {
	b.m.RLock()
	v := uint32(b.bm.GetCardinality())
	b.m.RUnlock()
	return v
}

func (b *Bitmap) Set(i uint32) {
	b.m.Lock()
	b.bm.Add(i)
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
	v := lo.Must(b.bm.MarshalBinary())
	b.m.RUnlock()
	return v
}

func (b *Bitmap) RangeX(fn func(uint32) bool) bool {
	b.m.RLock()
	defer b.m.RUnlock()
	i := b.bm.Iterator()
	for i.HasNext() {
		if !fn(i.Next()) {
			return true
		}
	}

	return false
}

func (b *Bitmap) Range(fn func(uint32)) {
	b.m.RLock()
	defer b.m.RUnlock()
	i := b.bm.Iterator()
	for i.HasNext() {
		fn(i.Next())
	}
}

// Bitfield return bytes as bittorrent protocol
func (b *Bitmap) Bitfield() []byte {
	b.m.RLock()
	v := b.bm.ToDense()
	b.m.RUnlock()

	var buf = make([]byte, 0, (b.size+7)/8)
	for _, u := range v {
		buf = binary.BigEndian.AppendUint64(buf, u)
	}

	return buf[:(b.size+7)/8]
}

func (b *Bitmap) XorRaw(bitmap *roaring.Bitmap) {
	b.m.Lock()
	b.bm.Xor(bitmap)
	b.m.Unlock()
}

func (b *Bitmap) OR(bm *Bitmap) {
	b.m.Lock()
	bm.m.RLock()
	b.bm.Or(bm.bm)
	bm.m.RUnlock()
	b.m.Unlock()
}

func (b *Bitmap) Clone() *Bitmap {
	b.m.RLock()
	m := b.bm.Clone()
	b.m.RUnlock()

	return FromBitmap(m, b.size)
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
	return b.bm.String()
}
