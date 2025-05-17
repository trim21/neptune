// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"encoding/binary"
	"io"
)

//go:generate go tool golang.org/x/tools/cmd/stringer -type=Message
type Message byte

const (
	Choke         Message = 0
	Unchoke       Message = 1
	Interested    Message = 2
	NotInterested Message = 3
	Have          Message = 4
	Bitfield      Message = 5
	Request       Message = 6
	Piece         Message = 7
	Cancel        Message = 8

	// BEP 5, for DHT.

	Port Message = 9

	// BEP 6 - Fast extension
	//https://www.bittorrent.org/beps/bep_0006.html

	Suggest     Message = 0x0d // payload piece index
	HaveAll     Message = 0x0e
	HaveNone    Message = 0x0f
	Reject      Message = 0x10
	AllowedFast Message = 0x11 // payload piece index

	// BEP 10
	//https://www.bittorrent.org/beps/bep_0010.html

	Extended Message = 20

	// BEP 52 - BitTorrent Protocol v2
	//https://www.bittorrent.org/beps/bep_0052.html

	// HashRequest Message = 21
	// Hashes      Message = 22
	// HashReject  Message = 23.

	BitCometExtension Message = 0xff
)

type ExtensionMessage byte

const (
	ExtensionHandshake ExtensionMessage = 0
)

func SendNoPayload(conn io.Writer, e Message) error {
	var b = make([]byte, 0, 5)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = append(b, byte(e))
	_, err := conn.Write(b)
	return err
}

// SendIndexOnly event with index only payload.
func SendIndexOnly(conn io.Writer, e Message, index uint32) error {
	var b = make([]byte, 0, 9)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = append(b, byte(e))
	b = binary.BigEndian.AppendUint32(b, index)
	_, err := conn.Write(b)
	return err
}

const sizeByte = 1
const sizeUint32 = 4
