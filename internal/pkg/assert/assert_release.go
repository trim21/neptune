// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package assert

func Equal[T comparable](v1, v2 T, msg ...string)    {}
func NotEqual[T comparable](v1, v2 T, msg ...string) {}
