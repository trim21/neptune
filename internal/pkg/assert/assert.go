// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package assert

import (
	"cmp"
)

func panicMessage(msg []string) {
	if len(msg) == 0 {
		panic("assert failed")
	}

	panic(msg[0])
}

func Equal[T comparable](actual, expected T, msg ...string) {
	if !(expected == actual) {
		panicMessage(msg)
	}
}

func NotEqual[T comparable](actual, expected T, msg ...string) {
	if !(expected != actual) {
		panicMessage(msg)
	}
}

func Less[T cmp.Ordered](v1, v2 T, msg ...string) {
	if !(v1 < v2) {
		panicMessage(msg)
	}
}
