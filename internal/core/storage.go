// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"crypto/sha1"

	"neptune/internal/meta"
	"neptune/internal/pkg/filepool"
)

// storeWriter is the internal interface for reading and writing torrent data.
// All methods use pieceIndex as the primary key, matching the BT protocol.
type storeWriter interface {
	// WriteChunk writes a block of data at [pieceIndex, begin] within the torrent.
	WriteChunk(pieceIndex uint32, begin uint32, data []byte) error
	// ReadChunk reads len(data) bytes of a block at [pieceIndex, begin].
	ReadChunk(pieceIndex uint32, begin uint32, data []byte) (int, error)
	// VerifyPiece reads the full piece, hashes it, and returns true if it matches.
	VerifyPiece(pieceIndex uint32, expected [sha1.Size]byte) (bool, error)
}

// fileStoreWriter is the production implementation backed by real files.
type fileStoreWriter struct {
	fp       *filepool.FilePool
	basePath string
	pieces   pieceInfo
	info     meta.Info
}

func newFileStoreWriter(info meta.Info, basePath string, fp *filepool.FilePool) *fileStoreWriter {
	return &fileStoreWriter{
		info:     info,
		basePath: basePath,
		fp:       fp,
		pieces:   buildPieceInfos(info),
	}
}
