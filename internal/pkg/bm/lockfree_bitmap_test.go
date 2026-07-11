// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm

import (
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
