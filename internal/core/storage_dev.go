//go:build !release

// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"crypto/sha1"
	"slices"
	"sort"
	"sync"

	"neptune/internal/meta"
)

// dev: pieceStore is the storeWriter interface — testable via mock injection.
type pieceStore = storeWriter

// memStoreWriter records writes in memory. Used by tests for zero-I/O fuzzing.
type memStoreWriter struct {
	data map[int64][]byte
	info meta.Info
	mu   sync.RWMutex
}

func newMemStoreWriter(info meta.Info) pieceStore {
	return &memStoreWriter{info: info, data: make(map[int64][]byte)}
}

func (s *memStoreWriter) WriteChunk(pieceIndex uint32, begin uint32, data []byte) error {
	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	cp := make([]byte, len(data))
	copy(cp, data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[offset] = cp
	return nil
}

func (s *memStoreWriter) ReadChunk(pieceIndex uint32, begin uint32, data []byte) (int, error) {
	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	s.mu.RLock()
	defer s.mu.RUnlock()
	chunk, ok := s.data[offset]
	if !ok {
		return 0, nil
	}
	return copy(data, chunk), nil
}

// VerifyPiece hashes stored data for the piece and compares.
func (s *memStoreWriter) VerifyPiece(pieceIndex uint32, expected [sha1.Size]byte) (bool, error) {
	offset := int64(pieceIndex) * s.info.PieceLength
	size := s.info.PieceLen(pieceIndex)

	s.mu.RLock()
	defer s.mu.RUnlock()

	var offsets []int64
	for off := range s.data {
		if off >= offset && off < offset+size {
			offsets = append(offsets, off)
		}
	}
	slices.Sort(offsets)

	var buf bytes.Buffer
	for _, off := range offsets {
		buf.Write(s.data[off])
	}

	digest := sha1.Sum(buf.Bytes())
	return digest == expected, nil
}
