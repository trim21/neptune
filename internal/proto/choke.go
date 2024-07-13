// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"

	"tyr/internal/pkg/ro"
)

var chokeMessage = ro.B(append(binary.BigEndian.AppendUint32(nil, 1), byte(Choke)))

func SendChoke(w io.Writer) error {
	_, err := chokeMessage.WriteTo(w)
	return err
}

var unchokeMessage = ro.B(append(binary.BigEndian.AppendUint32(nil, 1), byte(Unchoke)))

func SendUnchoke(w io.Writer) error {
	_, err := unchokeMessage.WriteTo(w)
	return err
}
