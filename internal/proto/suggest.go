// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

func SendSuggest(conn io.Writer, index uint32) error {
	var b = make([]byte, 0, 9)
	b = binary.BigEndian.AppendUint32(b, 5)
	b = append(b, byte(Suggest))
	b = binary.BigEndian.AppendUint32(b, index)
	_, err := conn.Write(b)
	return err
}
