// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import "sync/atomic"

// NilSafeLockFreeBitmap wraps a *LockFreeBitmap behind an atomic.Pointer.
// When the pointer is nil, all operations are no-ops (Contains returns false,
// Set/Unset/Clear do nothing, Count returns 0).
// Store(nil) atomically releases the backing array for GC.
type NilSafeLockFreeBitmap struct {
	p atomic.Pointer[LockFreeBitmap]
}

// NewNilSafeLockFreeBitmap creates a NilSafeLockFreeBitmap with the given size.
func NewNilSafeLockFreeBitmap(size uint32) *NilSafeLockFreeBitmap {
	b := &NilSafeLockFreeBitmap{}
	b.p.Store(NewLockFreeBitmap(size))
	return b
}

// Contains reports whether the bit at index i is set. Returns false when nil.
func (b *NilSafeLockFreeBitmap) Contains(i uint32) bool {
	bm := b.p.Load()
	if bm == nil {
		return false
	}
	return bm.Contains(i)
}

// Set sets the bit at index i. No-op when nil.
func (b *NilSafeLockFreeBitmap) Set(i uint32) {
	bm := b.p.Load()
	if bm != nil {
		bm.Set(i)
	}
}

// Unset clears the bit at index i. No-op when nil.
func (b *NilSafeLockFreeBitmap) Unset(i uint32) {
	bm := b.p.Load()
	if bm != nil {
		bm.Unset(i)
	}
}

// Clear clears all bits. No-op when nil.
func (b *NilSafeLockFreeBitmap) Clear() {
	bm := b.p.Load()
	if bm != nil {
		bm.Clear()
	}
}

// Count returns the number of set bits. Returns 0 when nil.
func (b *NilSafeLockFreeBitmap) Count() int {
	bm := b.p.Load()
	if bm == nil {
		return 0
	}
	return bm.Count()
}

// Release atomically stores nil, allowing the backing array to be GC'd.
// All subsequent operations become no-ops.
func (b *NilSafeLockFreeBitmap) Release() {
	b.p.Store(nil)
}

// Init creates a new LockFreeBitmap with the given size if the pointer
// is currently nil. No-op if already initialized.
func (b *NilSafeLockFreeBitmap) Init(size uint32) {
	if b.p.Load() == nil {
		b.p.Store(NewLockFreeBitmap(size))
	}
}
