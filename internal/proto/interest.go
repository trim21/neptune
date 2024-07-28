// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"io"

	"neptune/internal/pkg/ro"
)

var interested = ro.B([]byte{0, 0, 0, 1, byte(Interested)})

func SendInterested(w io.Writer) error {
	_, err := interested.WriteTo(w)
	return err
}

var notInterested = ro.B([]byte{0, 0, 0, 1, byte(NotInterested)})

func SendNotInterested(w io.Writer) error {
	_, err := notInterested.WriteTo(w)
	return err
}
