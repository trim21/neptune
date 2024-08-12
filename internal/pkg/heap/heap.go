// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package heap provides an implementation of a binary heap.
// A binary heap (binary min-heap) is a tree with the property that each node
// is the minimum-valued node in its subtree.
package heap

type Lesser[T any] interface {
	Less(b T) bool
}

// Heap implements a binary heap.
type Heap[T Lesser[T]] struct {
	Data []T
}

// New returns a new heap with the given less function.
func New[T Lesser[T]]() *Heap[T] {
	return &Heap[T]{
		Data: make([]T, 0),
	}
}

// FromSlice returns a new heap with the given less function and initial Data.
// The `Data` is not copied and used as the inside array.
func FromSlice[T Lesser[T]](data []T) *Heap[T] {
	n := len(data)
	for i := n/2 - 1; i >= 0; i-- {
		down(data, i)
	}

	return &Heap[T]{
		Data: data,
	}
}

// Push pushes the given element onto the heap.
func (h *Heap[T]) Push(x T) {
	h.Data = append(h.Data, x)
	up(h.Data, len(h.Data)-1)
}

// Pop removes and returns the minimum element from the heap.
// panic if slice is empty.
func (h *Heap[T]) Pop() T {
	x := h.Data[0]

	h.Data[0] = h.Data[len(h.Data)-1]
	h.Data = h.Data[:len(h.Data)-1]

	down(h.Data, 0)

	return x
}

// Peek returns the minimum element from the heap without removing it.
func (h *Heap[T]) Peek() T {
	return h.Data[0]
}

// Len returns the number of elements in the heap.
func (h *Heap[T]) Len() int {
	return len(h.Data)
}

func down[T Lesser[T]](h []T, i int) {
	for {
		left, right := 2*i+1, 2*i+2
		if left >= len(h) || left < 0 { // `left < 0` in case of overflow
			break
		}

		// find the smallest child
		j := left
		if right < len(h) && h[right].Less(h[left]) {
			j = right
		}

		if !h[j].Less(h[i]) {
			break
		}

		h[i], h[j] = h[j], h[i]
		i = j
	}
}

func up[T Lesser[T]](h []T, i int) {
	for {
		parent := (i - 1) / 2
		if i == 0 || !h[i].Less(h[parent]) {
			break
		}

		h[i], h[parent] = h[parent], h[i]
		i = parent
	}
}
