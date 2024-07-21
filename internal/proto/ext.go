// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto

import (
	"tyr/internal/pkg/null"
)

type ExtHandshake struct {
	V           null.String `bencode:"v,omitempty"`
	M           ExtM        `bencode:"m,omitempty"`
	QueueLength null.Uint32 `bencode:"reqq,omitempty"`
}

type ExtM struct {
	UTPex      null.Uint8                  `bencode:"ut_pex,omitempty"`
	LTDontHave null.Null[ExtensionMessage] `bencode:"lt_donthave,omitempty"`
}
