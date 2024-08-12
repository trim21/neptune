// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package metainfo

import (
	"crypto/sha1"
	"os"

	"github.com/trim21/go-bencode"
)

type MetaInfo struct {
	Announce string `bencode:"announce,omitempty"` // BEP 3
	Comment  string `bencode:"comment,omitempty"`

	// CreatedBy    string        `bencode:"created by,omitempty"`
	// Encoding     string        `bencode:"encoding,omitempty"`
	// UrlList UrlList `bencode:"url-list,omitempty"` // BEP 19 WebSeeds

	// Where's this specified? Mentioned at
	// https://wiki.theory.org/index.php/BitTorrentSpecification: (optional) the creation time of
	// the torrent, in standard UNIX epoch format (integer, seconds since 1-Jan-1970 00:00:00 UTC)
	// CreationDate null.Null[bencode.Total] `bencode:"creation date,omitempty,ignore_unmarshal_type_error"`
	InfoBytes    bencode.RawBytes `bencode:"info,omitempty"`          // BEP 3
	AnnounceList AnnounceList     `bencode:"announce-list,omitempty"` // BEP 12
}

// Load a MetaInfo from an io.Reader. Returns a non-nil error in case of failure.
func Load(raw []byte) (*MetaInfo, error) {
	var mi MetaInfo

	err := bencode.Unmarshal(raw, &mi)
	if err != nil {
		return nil, err
	}

	return &mi, nil
}

func LoadFromFile(filename string) (*MetaInfo, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var mi MetaInfo

	err = bencode.Unmarshal(raw, &mi)
	if err != nil {
		return nil, err
	}
	return &mi, nil
}

func (mi MetaInfo) UnmarshalInfo() (info Info, err error) {
	err = bencode.Unmarshal(mi.InfoBytes, &info)
	return
}

func (mi *MetaInfo) HashInfoBytes() Hash {
	return sha1.Sum(mi.InfoBytes)
}

func (mi *MetaInfo) UpvertedAnnounceList() AnnounceList {
	if mi.AnnounceList.OverridesAnnounce(mi.Announce) {
		return mi.AnnounceList
	}
	if mi.Announce != "" {
		return [][]string{{mi.Announce}}
	}
	return nil
}
