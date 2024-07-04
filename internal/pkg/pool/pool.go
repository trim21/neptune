// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// SPDX-License-Identifier: AGPL-3.0-only
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.
// See the GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>

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
