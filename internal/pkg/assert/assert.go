// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package assert

func panicMessage(msg []string) {
	if msg == nil || len(msg) == 0 {
		panic("assert failed")
	}

	panic(msg[0])
}

func Equal[T comparable](v1, v2 T, msg ...string) {
	if !(v1 == v2) {
		panicMessage(msg)
	}
}

func NotEqual[T comparable](v1, v2 T, msg ...string) {
	if !(v1 != v2) {
		panicMessage(msg)
	}
}
