// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package heap_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/heap"
)

type Int int

func (i Int) Less(o Int) bool {
	return i < o
}

func TestHeap(t *testing.T) {
	h := heap.New[Int]()
	h.Push(2)
	h.Push(1)
	h.Push(3)

	require.Equal(t, 3, h.Len())
	require.EqualValues(t, 1, h.Pop())
	require.Equal(t, 2, h.Len())
	h.Push(1)
	require.EqualValues(t, 1, h.Pop())
	require.EqualValues(t, 2, h.Pop())
	require.EqualValues(t, 3, h.Pop())
	require.Panics(t, func() { h.Pop() })
}

func TestHeap2(t *testing.T) {
	h := heap.FromSlice[Int]([]Int{1, 2, 3})

	h.Push(1)

	require.Equal(t, 4, h.Len())
	require.EqualValues(t, 1, h.Pop())
	require.Equal(t, 3, h.Len())
}
