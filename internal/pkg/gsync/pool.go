// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gsync

import (
	"sync"
)

type Pool[T any] struct {
	pool sync.Pool
}

//nolint:forcetypeassert
func (p *Pool[T]) Get() T {
	return p.pool.Get().(T)
}

func (p *Pool[T]) Put(t T) {
	p.pool.Put(t)
}

func NewPool[T any, F func() T](fn F) *Pool[T] {
	if fn == nil {
		panic("missing new function")
	}

	return &Pool[T]{
		pool: sync.Pool{
			New: func() any {
				return fn()
			},
		},
	}
}
