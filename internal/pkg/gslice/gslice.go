// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gslice

func Remove[T comparable](l []T, item T) []T {
	for i, other := range l {
		if other == item {
			copy(l[i:], l[i+1:])
			clear(l[len(l)-1:]) // avoid reference to item to avoid GC leak
			return l[:len(l)-1]
		}
	}
	return l
}
