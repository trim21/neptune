// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

func SendHave(conn io.Writer, pieceIndex uint32) error {
	buf := smallBufPool.Get()
	defer smallBufPool.Put(buf)

	buf.B = binary.BigEndian.AppendUint32(buf.B, 5)
	buf.B = append(buf.B, byte(Have))
	buf.B = binary.BigEndian.AppendUint32(buf.B, pieceIndex)
	_, err := conn.Write(buf.B)
	return err
}
