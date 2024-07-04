// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package metainfo

// Information specific to a single file inside the MetaInfo structure.
type FileInfo struct {
	// BEP3. With BEP 47 this can be optional, but we have no way to describe that without breaking
	// the API.
	Length int64    `bencode:"length"`
	Path   []string `bencode:"path"` // BEP3
	// Unofficial extension by BiglyBT? https://github.com/BiglySoftware/BiglyBT/issues/1274. Might
	// be a safer bet when available: https://github.com/anacrolix/torrent/pull/915.
	PathUtf8 []string `bencode:"path.utf-8,omitempty"`

	TorrentOffset int64 `bencode:"-"`
}

func (fi *FileInfo) BestPath() []string {
	if len(fi.PathUtf8) != 0 {
		return fi.PathUtf8
	}
	return fi.Path
}
