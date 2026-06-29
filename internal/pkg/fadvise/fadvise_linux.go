// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package fadvise

import (
	"os"

	"golang.org/x/sys/unix"
)

// Fadvise applies an advisory hint about the access pattern to the kernel
// for the given file descriptor and byte range.
func Fadvise(fd uintptr, offset int64, length int64, advice int) error {
	return unix.Fadvise(int(fd), offset, length, advice)
}

// DontNeed tells the kernel that the specified byte range will not be
// accessed in the near future. The kernel may free resources associated
// with this range, such as page cache entries.
func DontNeed(file *os.File, offset int64, length int64) error {
	return unix.Fadvise(int(file.Fd()), offset, length, AdvDontNeed)
}

// WillNeed tells the kernel that the specified byte range will be
// accessed in the near future, allowing the kernel to prefetch data.
func WillNeed(file *os.File, offset int64, length int64) error {
	return unix.Fadvise(int(file.Fd()), offset, length, AdvWillNeed)
}

// Sequential tells the kernel that the file will be accessed sequentially.
func Sequential(file *os.File, offset int64, length int64) error {
	return unix.Fadvise(int(file.Fd()), offset, length, AdvSequential)
}

// Random tells the kernel that the file will be accessed in random order.
func Random(file *os.File, offset int64, length int64) error {
	return unix.Fadvise(int(file.Fd()), offset, length, AdvRandom)
}

// NoReuse tells the kernel that data will be accessed only once.
func NoReuse(file *os.File, offset int64, length int64) error {
	return unix.Fadvise(int(file.Fd()), offset, length, AdvNoReuse)
}
