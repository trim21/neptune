// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package fallocate

import (
	"os"
	"syscall"
)

func Fallocate(file *os.File, offset int64, length int64) error {
	if length == 0 {
		return nil
	}

	return syscall.Fallocate(int(file.Fd()), 0, offset, length)
}
