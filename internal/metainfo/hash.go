// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package metainfo

import (
	"encoding/hex"

	"tyr/internal/pkg/unsafe"
)

type Hash [20]byte

func (h Hash) Bytes() []byte { return h[:] }

func (h Hash) AsString() string {
	return unsafe.Str(h[:])
}

func (h Hash) String() string {
	return h.Hex()
}

func (h Hash) Hex() string {
	return hex.EncodeToString(h[:])
}
