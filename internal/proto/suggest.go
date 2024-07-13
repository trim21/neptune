// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

func SendSuggest(conn io.Writer, index uint32) error {
	buf := pool.Get()
	defer pool.Put(buf)

	buf.B = binary.BigEndian.AppendUint32(buf.B, 5)
	buf.B = append(buf.B, byte(Suggest))
	buf.B = binary.BigEndian.AppendUint32(buf.B, index)

	_, err := conn.Write(buf.B)

	return err
}
