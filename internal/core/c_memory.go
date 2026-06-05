// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"go.uber.org/atomic"
)

// memoryBudget is a soft memory limit enforced via atomic counter.
// Callers acquire bytes before allocating cache/buffer memory and
// release bytes when the cached data is flushed or consumed.
// When the budget is exhausted, TryAcquire returns false and callers
// should shed load (drop responses, skip uploads).
type memoryBudget struct {
	limit int64
	used  atomic.Int64
}

func newMemoryBudget(limit int64) *memoryBudget {
	return &memoryBudget{limit: limit}
}

// TryAcquire attempts to reserve n bytes. Returns false if over limit.
func (b *memoryBudget) TryAcquire(n int64) bool {
	if b == nil || b.limit <= 0 {
		return true
	}

	for {
		old := b.used.Load()
		newVal := old + n
		if newVal > b.limit {
			return false
		}

		if b.used.CompareAndSwap(old, newVal) {
			return true
		}
	}
}

// ForceAcquire reserves n bytes unconditionally. May exceed the limit.
// Use when data is already allocated and cannot be dropped.
func (b *memoryBudget) ForceAcquire(n int64) {
	if b == nil || b.limit <= 0 {
		return
	}

	b.used.Add(n)
}

// Release returns n bytes to the budget.
func (b *memoryBudget) Release(n int64) {
	if b == nil || b.limit <= 0 {
		return
	}

	b.used.Add(-n)
}

// Available returns true if usage is under the limit.
func (b *memoryBudget) Available() bool {
	if b == nil || b.limit <= 0 {
		return true
	}

	return b.used.Load() < b.limit
}

// Used returns currently reserved bytes.
func (b *memoryBudget) Used() int64 {
	if b == nil {
		return 0
	}

	return b.used.Load()
}
