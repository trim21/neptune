// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package metainfo

type Info struct {
	Private *bool `bencode:"private,omitempty"` // BEP27

	Name     string `bencode:"name"` // BEP3
	NameUtf8 string `bencode:"name.utf-8,omitempty"`

	Source string     `bencode:"source,omitempty"`
	Pieces []byte     `bencode:"pieces"`          // BEP3
	Files  []FileInfo `bencode:"files,omitempty"` // BEP3, mutually exclusive with Length

	PieceLength int64 `bencode:"piece length"`     // BEP3
	Length      int64 `bencode:"length,omitempty"` // BEP3, mutually exclusive with Files

	// BEP 52 (BitTorrent v2)
	MetaVersion int `bencode:"meta version,omitempty"`
}

func (info *Info) TotalLength() int64 {
	if len(info.Files) == 0 {
		return info.Length
	}

	var ret int64
	for _, fi := range info.Files {
		ret += fi.Length
	}

	return ret
}

func (info *Info) NumPieces() (num int) {
	return len(info.Pieces) / 20
}

func (info *Info) BestName() string {
	if info.NameUtf8 != "" {
		return info.NameUtf8
	}
	return info.Name
}
