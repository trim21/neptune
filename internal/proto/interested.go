// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"io"
)

func SendInterested(w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 1, byte(Interested)})
	return err
}

func SendNotInterested(w io.Writer) error {
	_, err := w.Write([]byte{0, 0, 0, 1, byte(NotInterested)})
	return err
}
