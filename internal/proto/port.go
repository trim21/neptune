// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

func SendPort(conn io.Writer, port uint16) error {
	var b = make([]byte, 0, 4+1+2)

	b = binary.BigEndian.AppendUint32(b, 3)
	b = append(b, byte(Port))
	b = binary.BigEndian.AppendUint16(b, port)

	_, err := conn.Write(b)

	return err
}
