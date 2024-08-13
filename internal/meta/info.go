// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"path/filepath"

	"github.com/samber/lo"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/null"
)

type File struct {
	Path    string
	RawPath []string
	Length  int64
}

type Info struct {
	Name          string
	Comment       string
	Pieces        []metainfo.Hash
	Files         []File
	TotalLength   int64
	PieceLength   int64
	LastPieceSize int64
	NumPieces     uint32
	Hash          metainfo.Hash
	HexHash       string
	Private       bool
}

var ErrNotV1Torrent = errors.New("torrent is not valid torrent, only v1 torrent is supported")
var ErrInvalidLength = errors.New("torrent has invalid length")

func FromTorrent(m metainfo.MetaInfo) (Info, error) {
	info, err := m.UnmarshalInfo()
	if err != nil {
		return Info{}, err
	}

	if len(info.Pieces) == 0 {
		return Info{}, ErrNotV1Torrent
	}
	if len(info.Files) == 0 && info.Length == 0 {
		return Info{}, ErrNotV1Torrent
	}

	if len(info.Pieces)%sha1.Size != 0 {
		return Info{}, errors.New("invalid pieces length, len(info.pieces)%sha1.Size != 0")
	}

	var pieces = make([]metainfo.Hash, info.NumPieces())
	for i := range info.NumPieces() {
		pieces[i] = metainfo.Hash(info.Pieces[i*sha1.Size : (i+1)*sha1.Size])
	}

	var files []File
	if len(info.Files) != 0 {
		files = lo.Map(info.Files, func(item metainfo.FileInfo, index int) File {
			return File{
				Path:    filepath.Join(item.BestPath()...),
				RawPath: item.BestPath(),
				Length:  item.Length,
			}
		})
	} else {
		files = []File{
			{
				Path:   info.BestName(),
				Length: info.TotalLength(),
			},
		}
	}

	h := m.HashInfoBytes()

	i := Info{
		Hash:          h,
		Private:       null.NewFromPtr(info.Private).Value,
		Name:          info.BestName(),
		TotalLength:   info.TotalLength(),
		Pieces:        pieces,
		NumPieces:     uint32(info.NumPieces()),
		HexHash:       hex.EncodeToString(h[:]),
		PieceLength:   info.PieceLength,
		LastPieceSize: info.TotalLength() - info.PieceLength*int64(info.NumPieces()-1),
		Files:         files,
		Comment:       m.Comment,
	}

	if int64(i.NumPieces) != (i.TotalLength+i.PieceLength-1)/i.PieceLength {
		return Info{}, ErrInvalidLength
	}

	return i, nil
}
