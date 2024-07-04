// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"

	"tyr/internal/pkg/bm"
)

func SendBitfield(conn io.Writer, bm *bm.Bitmap) error {
	b := bm.Bitfield()

	err := binary.Write(conn, binary.BigEndian, uint32(1+len(b)))
	if err != nil {
		return err
	}

	_, err = conn.Write([]byte{byte(Bitfield)})

	if err != nil {
		return err
	}

	_, err = conn.Write(b)

	return err
}
