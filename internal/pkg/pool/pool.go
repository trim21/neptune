// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package pool

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

func New[T any, F func() T](fn F) *Pool[T] {
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

func NewWithReset[T any, F func() T, R func(T) bool](fn F, reset R) *WithReset[T] {
	if fn == nil {
		panic("missing new function")
	}
	if reset == nil {
		panic("missing reset function")
	}
	return &WithReset[T]{
		reset: reset,
		pool: sync.Pool{
			New: func() any {
				return fn()
			},
		},
	}
}

type WithReset[T any] struct {
	pool  sync.Pool
	reset func(T) bool
}

//nolint:forcetypeassert
func (p *WithReset[T]) Get() T {
	return p.pool.Get().(T)
}

func (p *WithReset[T]) Put(t T) {
	if p.reset(t) {

		p.pool.Put(t)
	}
}
