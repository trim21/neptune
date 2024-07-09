// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package filepool

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

func CanIgnore(err error) bool {
	return errors.Is(err, syscall.EMFILE)
}

func Fadvise(fd int) {
	unix.Fadvise(fd, 0, 0, unix.FADV_RANDOM)
}
