// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package filepool

func CanIgnore(err error) bool {
	return false
}

func Fadvise(fd int) {
	return
}
