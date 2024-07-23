// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"io"

	"neptune/internal/pkg/ro"
)

var keepAlive = ro.B([]byte{0, 0, 0, 0})

func SendKeepAlive(w io.Writer) error {
	_, err := keepAlive.WriteTo(w)
	return err
}
