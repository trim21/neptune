// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

var chokeMessage = func() []byte {
	b := binary.BigEndian.AppendUint32(nil, 1)
	b = append(b, byte(Choke))
	return b
}()

func SendChoke(w io.Writer) error {
	_, err := w.Write(chokeMessage)
	return err
}

var unchokeMessage = func() []byte {
	b := binary.BigEndian.AppendUint32(nil, 1)
	b = append(b, byte(Unchoke))
	return b
}()

func SendUnchoke(w io.Writer) error {
	_, err := w.Write(unchokeMessage)
	return err
}
