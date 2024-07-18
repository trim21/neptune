// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build windows

package fallocate

import (
	"os"
)

func Fallocate(file *os.File, offset int64, length int64) error {
	if length == 0 {
		return nil
	}

	err := file.Truncate(length + offset)
	if err != nil {
		return err
	}

	return file.Sync()
}
