// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gsync

import (
	"sync"

	"go.uber.org/atomic"
)

type Map[K comparable, V any] struct {
	m     sync.Map
	count atomic.Int64
}

func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		m: sync.Map{},
	}
}

func (m *Map[K, V]) Load(k K) (v V, ok bool) {
	vv, ok := m.m.Load(k)
	if ok {
		return vv.(V), ok
	}

	return
}

func (m *Map[K, V]) Store(k K, v V) {
	_, exists := m.m.Swap(k, v)
	if !exists {
		m.count.Inc()
	}
}

func (m *Map[K, V]) Size() int {
	return int(m.count.Load())
}

func (m *Map[K, V]) LoadOrStore(k K, v V) (V, bool) {
	vv, exists := m.m.Swap(k, v)
	if exists {
		return vv.(V), exists
	}

	m.count.Inc()

	var zero V
	return zero, false
}

func (m *Map[K, V]) LoadAndDelete(k K) (v V, ok bool) {
	previous, e := m.m.LoadAndDelete(k)
	if e {
		m.count.Dec()
		return previous.(V), true
	}

	return v, false
}

func (m *Map[K, V]) Range(fn func(k K, v V) bool) {
	m.m.Range(func(k, v any) bool {
		return fn(k.(K), v.(V))
	})
}
