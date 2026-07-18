// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_store

import (
	"context"
	"crypto/sha1"
	"sync"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/gfs"
)

// Store is the interface for reading and writing torrent piece data.
// All methods use pieceIndex as the primary key, matching the BT protocol.
type Store interface {
	WriteChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) error
	ReadChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error)
	VerifyPiece(ctx context.Context, pieceIndex uint32, expected [sha1.Size]byte) (bool, error)
	Move(ctx context.Context, target string, report MoveProgressFunc) error
}

type MovePhase uint8

const (
	MoveWaiting MovePhase = iota
	MoveCopying
	MoveCommitting
	MoveCleaning
)

type MoveProgress struct {
	Phase       MovePhase
	FilesDone   int
	FilesTotal  int
	BytesDone   int64
	BytesTotal  int64
	BytesCopied int64
}

// MoveProgressFunc receives synchronous progress snapshots. It must not block
// or call back into the Store.
type MoveProgressFunc func(MoveProgress)

// FileStore is the production implementation backed by real files via filepool.
type FileStore struct {
	fp               *filepool.FilePool
	selectedFilesSet *bm.Bitmap
	fallocatedBm     *bm.LockFreeBitmap
	ioc              *gfs.IOContext
	diskIO           *gfs.PathIO
	basePath         string
	info             meta.Info
	fallocate        bool
	opMu             sync.RWMutex
}

// NewFileStore creates a FileStore for the given torrent info and base path.
func NewFileStore(info meta.Info, basePath string, fp *filepool.FilePool, ioc *gfs.IOContext, selectedFilesSet *bm.Bitmap, fallocate bool) *FileStore {
	return &FileStore{
		info:             info,
		basePath:         basePath,
		fp:               fp,
		ioc:              ioc,
		diskIO:           ioc.ForPath(basePath),
		selectedFilesSet: selectedFilesSet,
		fallocatedBm:     bm.NewLockFreeBitmap(uint32(len(info.Files))),
		fallocate:        fallocate,
	}
}
