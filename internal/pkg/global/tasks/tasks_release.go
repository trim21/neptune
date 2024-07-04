// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package tasks

func Submit(task func()) {
	_ = pool.Submit(task)
}
