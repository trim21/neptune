// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gslice

import (
	"cmp"
)

func Remove[T comparable](l []T, item T) []T {
	for i, other := range l {
		if other == item {
			return append(l[:i], l[i+1:]...)
		}
	}
	return l
}

func Max[S ~[]E, E cmp.Ordered](x S) (int, E) {
	if len(x) < 1 {
		panic("slices.Max: empty list")
	}

	m := x[0]
	var index = 0

	for i := 1; i < len(x); i++ {
		if x[i] > m {
			index = i
			m = x[i]
		}
	}

	return index, m
}
