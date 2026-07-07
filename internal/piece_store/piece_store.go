// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_store

import (
	"crypto/sha1"

	"neptune/internal/meta"
	"neptune/internal/pkg/filepool"
)

// Store is the interface for reading and writing torrent piece data.
// All methods use pieceIndex as the primary key, matching the BT protocol.
type Store interface {
	WriteChunk(pieceIndex uint32, begin uint32, data []byte) error
	ReadChunk(pieceIndex uint32, begin uint32, data []byte) (int, error)
	VerifyPiece(pieceIndex uint32, expected [sha1.Size]byte) (bool, error)
}

// FileStore is the production implementation backed by real files via filepool.
type FileStore struct {
	fp       *filepool.FilePool
	basePath string
	pieces   meta.PieceInfo
	info     meta.Info
}

// NewFileStore creates a FileStore for the given torrent info and base path.
func NewFileStore(info meta.Info, basePath string, fp *filepool.FilePool) *FileStore {
	return &FileStore{
		info:     info,
		basePath: basePath,
		fp:       fp,
		pieces:   meta.BuildPieceInfos(info),
	}
}
