// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package fadvise

import "os"

// Fadvise is a no-op on non-Linux platforms.
func Fadvise(fd uintptr, offset int64, length int64, advice int) error {
	return nil
}

// DontNeed is a no-op on non-Linux platforms.
func DontNeed(file *os.File, offset int64, length int64) error {
	return nil
}

// WillNeed is a no-op on non-Linux platforms.
func WillNeed(file *os.File, offset int64, length int64) error {
	return nil
}

// Sequential is a no-op on non-Linux platforms.
func Sequential(file *os.File, offset int64, length int64) error {
	return nil
}

// Random is a no-op on non-Linux platforms.
func Random(file *os.File, offset int64, length int64) error {
	return nil
}

// NoReuse is a no-op on non-Linux platforms.
func NoReuse(file *os.File, offset int64, length int64) error {
	return nil
}
