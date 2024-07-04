// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"encoding"

	"github.com/anacrolix/torrent/bencode"
)

var _ encoding.BinaryMarshaler = (*Download)(nil)
var _ encoding.BinaryUnmarshaler = (*Download)(nil)

type resume struct {
	BasePath    string
	Bitmap      []byte
	Tags        []string
	AddAt       int64
	CompletedAt int64
	Downloaded  int64
	Uploaded    int64
	State       State
}

func (d *Download) MarshalBinary() (data []byte, err error) {
	return bencode.Marshal(resume{
		BasePath:    d.basePath,
		Downloaded:  d.downloaded.Load(),
		Uploaded:    d.uploaded.Load(),
		Tags:        d.tags,
		State:       d.state,
		AddAt:       d.AddAt,
		CompletedAt: d.CompletedAt.Load(),
		Bitmap:      d.bm.CompressedBytes(),
	})
}

func (d *Download) UnmarshalBinary(data []byte) error {
	//TODO implement me
	panic("implement me")
}
