// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
	"slices"
	"sync"
	"testing"
)

func TestLockFreeBitmap_Basic(t *testing.T) {
	b := NewLockFreeBitmap(128)

	// Initially all bits are 0
	for i := range uint32(128) {
		if b.Contains(i) {
			t.Fatalf("bit %d should be unset", i)
		}
	}

	// Set some bits
	b.Set(0)
	b.Set(63)
	b.Set(64)
	b.Set(127)

	if !b.Contains(0) || !b.Contains(63) || !b.Contains(64) || !b.Contains(127) {
		t.Fatal("set bits should be contained")
	}

	// Unset a bit
	b.Unset(63)
	if b.Contains(63) {
		t.Fatal("bit 63 should be unset after Unset")
	}

	// Other bits should be unaffected
	if !b.Contains(0) || !b.Contains(64) || !b.Contains(127) {
		t.Fatal("other bits should be unaffected")
	}
}

func TestLockFreeBitmap_SetOperations(t *testing.T) {
	b := NewLockFreeBitmap(130)
	b.Set(1)
	b.Set(65)
	b.Set(129)

	var ranged []uint32
	b.Range(func(index uint32) {
		ranged = append(ranged, index)
	})
	if !slices.Equal(ranged, []uint32{1, 65, 129}) {
		t.Fatalf("Range() = %v", ranged)
	}
	if array := b.ToArray(); !slices.Equal(array, ranged) {
		t.Fatalf("ToArray() = %v, want %v", array, ranged)
	}

	other := New(130)
	other.Set(65)
	merged := NewLockFreeBitmap(130)
	merged.OR(other)
	if !merged.Contains(65) || merged.Count() != 1 {
		t.Fatalf("OR result = %v", merged.ToArray())
	}
	if !b.Any(merged) {
		t.Fatal("Any should find bit 65")
	}
}

func TestLockFreeBitmap_Concurrent(t *testing.T) {
	b := NewLockFreeBitmap(1024)
	var wg sync.WaitGroup

	// Concurrent writers
	for i := range uint32(1024) {
		wg.Add(1)
		go func(idx uint32) {
			defer wg.Done()
			b.Set(idx)
		}(i)
	}
	wg.Wait()

	// Verify all bits are set
	for i := range uint32(1024) {
		if !b.Contains(i) {
			t.Fatalf("bit %d should be set", i)
		}
	}

	// Concurrent readers
	for i := range uint32(1024) {
		wg.Add(1)
		go func(idx uint32) {
			defer wg.Done()
			if !b.Contains(idx) {
				t.Errorf("bit %d should be set", idx)
			}
		}(i)
	}
	wg.Wait()
}
