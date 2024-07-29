// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"neptune/internal/pkg/null"
)

type ExtHandshake struct {
	V           null.String `bencode:"v,omitempty"`
	Mapping     ExtMapping  `bencode:"m,omitempty"` // mapping from supported name to extension id
	QueueLength null.Uint32 `bencode:"reqq,omitempty"`
	UploadOnly  null.Bool   `bencode:"upload_only,omitempty"`
}

type ExtMapping struct {
	Pex      null.Null[ExtensionMessage] `bencode:"ut_pex,omitempty"`
	DontHave null.Null[ExtensionMessage] `bencode:"lt_donthave,omitempty"`
}

// http://bittorrent.org/beps/bep_0011.html

const PexFlagPreferEnc = 0x01
const PexFlagSeedOnly = 0x02
const PexFlagSupportUTP = 0x04
const PexFlagSupportHolePunchP = 0x08
const PexFlagOutgoing = 0x10

type ExtPex struct {
	// compact ipv4 addr port
	Added []byte `bencode:"added,omitempty"`
	// flag for [Added]
	AddedFlag []byte `bencode:"added.f,omitempty"`
	// compact ipv6 addr port
	Added6 []byte `bencode:"added6,omitempty"`
	// flag for [Added6]
	Added6Flag []byte `bencode:"added6.f,omitempty"`
	// compact ipv4 addr port
	Dropped []byte `bencode:"dropped,omitempty"`
	// compact ipv6 addr port
	Dropped6 []byte `bencode:"dropped6,omitempty"`
}
