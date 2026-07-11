// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
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
