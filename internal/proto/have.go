// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

func SendHave(conn io.Writer, pieceIndex uint32) error {
	var b = make([]byte, 0, 9)
	b = binary.BigEndian.AppendUint32(b, 5)
	b = append(b, byte(Have))
	b = binary.BigEndian.AppendUint32(b, pieceIndex)
	_, err := conn.Write(b)
	return err
}
