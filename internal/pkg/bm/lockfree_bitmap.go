// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
	"math/bits"
	"sync/atomic"
)

// LockFreeBitmap is a concurrent bitmap optimized for read-heavy workloads.
// Reads (Contains) are lock-free using atomic loads.
// Writes (Set/Unset) are also lock-free using atomic operations.
type LockFreeBitmap struct {
	words []atomic.Uint64
	size  uint32
}

// NewLockFreeBitmap creates a new LockFreeBitmap with the given number of bits.
func NewLockFreeBitmap(size uint32) *LockFreeBitmap {
	numWords := (size + 63) / 64
	return &LockFreeBitmap{
		words: make([]atomic.Uint64, numWords),
		size:  size,
	}
}

// Contains reports whether the bit at index i is set.
func (b *LockFreeBitmap) Contains(i uint32) bool {
	word := i / 64
	bit := i % 64
	return (b.words[word].Load()>>bit)&1 == 1
}

// Set sets the bit at index i.
func (b *LockFreeBitmap) Set(i uint32) {
	word := i / 64
	bit := i % 64
	b.words[word].Or(1 << bit)
}

// Unset clears the bit at index i.
func (b *LockFreeBitmap) Unset(i uint32) {
	word := i / 64
	bit := i % 64
	b.words[word].And(^(uint64(1) << bit))
}

// Fill sets all bits in [0, size).
func (b *LockFreeBitmap) Fill() {
	fullWords := b.size / 64
	remainder := b.size % 64

	for i := range fullWords {
		b.words[i].Store(^uint64(0))
	}

	if remainder > 0 {
		b.words[fullWords].Store(^uint64(0) >> (64 - remainder))
	}
}

// Clear clears all bits.
func (b *LockFreeBitmap) Clear() {
	for i := range b.words {
		b.words[i].Store(0)
	}
}

// Count returns the number of set bits.
func (b *LockFreeBitmap) Count() int {
	if len(b.words) == 0 {
		return 0
	}
	n := 0
	for i := range b.words[:len(b.words)-1] {
		n += bits.OnesCount64(b.words[i].Load())
	}
	last := b.words[len(b.words)-1].Load()
	remainder := b.size % 64
	if remainder > 0 {
		last &= ^uint64(0) >> (64 - remainder)
	}
	n += bits.OnesCount64(last)
	return n
}

// OR adds every bit set in other. Reads of b may observe the update one word
// at a time, which is sufficient for availability tracking.
func (b *LockFreeBitmap) OR(other *Bitmap) {
	other.m.RLock()
	defer other.m.RUnlock()

	for i := range min(len(b.words), len(other.bm)) {
		b.words[i].Or(other.bm[i])
	}
}

// Any reports whether b and other have at least one common set bit.
func (b *LockFreeBitmap) Any(other *LockFreeBitmap) bool {
	if b == nil || other == nil {
		return false
	}
	for i := range min(len(b.words), len(other.words)) {
		if b.words[i].Load()&other.words[i].Load() != 0 {
			return true
		}
	}
	return false
}

// Range calls fn for every set bit. Concurrent writes may be observed between
// words; callers that need a coherent snapshot must provide their own gate.
func (b *LockFreeBitmap) Range(fn func(uint32)) {
	for wordIndex := range b.words {
		v := b.words[wordIndex].Load()
		for v != 0 {
			bit := bits.TrailingZeros64(v)
			index := uint32(wordIndex*64 + bit)
			if index >= b.size {
				return
			}
			fn(index)
			v &^= 1 << bit
		}
	}
}

func (b *LockFreeBitmap) ToArray() []uint32 {
	result := make([]uint32, 0, b.Count())
	b.Range(func(index uint32) {
		result = append(result, index)
	})
	return result
}
