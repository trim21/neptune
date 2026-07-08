// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

import (
	"crypto/sha1"
	"errors"
	"iter"
	"path/filepath"
	"sort"

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
	fileOffsets   []int64 // cumulative byte offsets, len(Files)+1
	TotalLength   int64
	PieceLength   int64
	LastPieceSize int64
	NumPieces     uint32
	Hash          metainfo.Hash
	Private       bool
}

const DefaultBlockSize = 16 * 1024

// PieceLen returns the byte length of the piece at the given index.
// DefaultBlockSize is the standard BitTorrent block size (16 KiB).
func (info *Info) PieceLen(index uint32) int64 {
	if index == info.NumPieces-1 {
		return info.LastPieceSize
	}
	return info.PieceLength
}

// PieceFileChunks returns an iterator over the file chunks for a piece.
// Zero allocations.
func (info *Info) PieceFileChunks(pieceIndex uint32) iter.Seq[FileChunkInfo] {
	start := int64(pieceIndex) * info.PieceLength
	return info.FileChunks(start, start+info.PieceLen(pieceIndex))
}

// PieceBlockCount returns the number of default-block-size blocks in the piece.
func (info *Info) PieceBlockCount(index uint32) int {
	length := info.PieceLength
	if index == info.NumPieces-1 {
		length = info.LastPieceSize
	}
	return int((length + DefaultBlockSize - 1) / DefaultBlockSize)
}

// FileChunks returns an iterator over contiguous byte ranges within [start, end).
// Zero allocations; the FileChunkInfo struct is passed on the stack.
func (info *Info) FileChunks(start, end int64) iter.Seq[FileChunkInfo] {
	return func(yield func(FileChunkInfo) bool) {
		if start >= end || len(info.Files) == 0 {
			return
		}
		idx := sort.Search(len(info.Files), func(i int) bool {
			return info.fileOffsets[i+1] > start
		})
		for start < end && idx < len(info.Files) {
			fileStart := info.fileOffsets[idx]
			fileEnd := info.fileOffsets[idx+1]
			chunkEnd := min(end, fileEnd)
			if !yield(FileChunkInfo{FileIndex: idx, OffsetOfFile: start - fileStart, Length: chunkEnd - start}) {
				return
			}
			start = chunkEnd
			idx++
		}
	}
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
			rawPath := item.BestPath()
			for i, c := range rawPath {
				rawPath[i] = SafePathComponent(c)
			}
			return File{
				Path:    filepath.Join(rawPath...),
				RawPath: rawPath,
				Length:  item.Length,
			}
		})
	} else {
		name := SafePathComponent(info.BestName())
		files = []File{
			{
				Path:    name,
				RawPath: []string{name},
				Length:  info.TotalLength(),
			},
		}
	}

	i := Info{
		Hash:          m.HashInfoBytes(),
		Private:       null.NewFromPtr(info.Private).Value,
		Name:          info.BestName(),
		TotalLength:   info.TotalLength(),
		Pieces:        pieces,
		NumPieces:     uint32(info.NumPieces()),
		PieceLength:   info.PieceLength,
		LastPieceSize: info.TotalLength() - info.PieceLength*int64(info.NumPieces()-1),
		Files:         files,
		Comment:       m.Comment,
	}

	if int64(i.NumPieces) != (i.TotalLength+i.PieceLength-1)/i.PieceLength {
		return Info{}, ErrInvalidLength
	}

	// Precompute cumulative file offsets for O(log N) file lookups.
	i.fileOffsets = make([]int64, len(i.Files)+1)
	var off int64
	for idx, f := range i.Files {
		i.fileOffsets[idx] = off
		off += f.Length
	}
	i.fileOffsets[len(i.Files)] = off

	return i, nil
}
