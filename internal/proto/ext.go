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
	Added      []byte `json:"added,omitempty"`
	AddedFlag  []byte `json:"added.f,omitempty"`
	Added6     []byte `json:"added6,omitempty"`
	Added6Flag []byte `json:"added6.f,omitempty"`
	Dropped    []byte `json:"dropped,omitempty"`
	Dropped6   []byte `json:"dropped6,omitempty"`
}
