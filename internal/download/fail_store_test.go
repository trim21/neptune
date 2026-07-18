// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"sync"

	"neptune/internal/piece_store"
)

// FailOnceStore wraps a PieceStore and fails VerifyPiece for each piece
// the first time it's called. Subsequent verifications for the same piece
// pass through to the underlying store.
type FailOnceStore struct {
	inner  piece_store.Store
	failed map[uint32]bool
	mu     sync.Mutex
}

func NewFailOnceStore(inner piece_store.Store) *FailOnceStore {
	return &FailOnceStore{
		inner:  inner,
		failed: make(map[uint32]bool),
	}
}

func (s *FailOnceStore) WriteChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) error {
	return s.inner.WriteChunk(ctx, pieceIndex, begin, data)
}

func (s *FailOnceStore) ReadChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error) {
	return s.inner.ReadChunk(ctx, pieceIndex, begin, data)
}

func (s *FailOnceStore) VerifyPiece(ctx context.Context, pieceIndex uint32, expected [20]byte) (bool, error) {
	s.mu.Lock()
	if !s.failed[pieceIndex] {
		s.failed[pieceIndex] = true
		s.mu.Unlock()
		return false, nil // first time: fail
	}
	s.mu.Unlock()
	return s.inner.VerifyPiece(ctx, pieceIndex, expected)
}

func (s *FailOnceStore) Move(ctx context.Context, target string, report piece_store.MoveProgressFunc) error {
	return s.inner.Move(ctx, target, report)
}

// FailNPieceStore wraps a PieceStore and fails the first N pieces
// on their first verification.
type FailNPieceStore struct {
	inner   piece_store.Store
	failSet map[uint32]bool
	failed  map[uint32]bool
	mu      sync.Mutex
}

func NewFailNPieceStore(inner piece_store.Store, failPieces []uint32) *FailNPieceStore {
	failSet := make(map[uint32]bool)
	for _, pi := range failPieces {
		failSet[pi] = true
	}
	return &FailNPieceStore{
		inner:   inner,
		failSet: failSet,
		failed:  make(map[uint32]bool),
	}
}

func (s *FailNPieceStore) WriteChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) error {
	return s.inner.WriteChunk(ctx, pieceIndex, begin, data)
}

func (s *FailNPieceStore) ReadChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error) {
	return s.inner.ReadChunk(ctx, pieceIndex, begin, data)
}

func (s *FailNPieceStore) VerifyPiece(ctx context.Context, pieceIndex uint32, expected [20]byte) (bool, error) {
	s.mu.Lock()
	if s.failSet[pieceIndex] && !s.failed[pieceIndex] {
		s.failed[pieceIndex] = true
		s.mu.Unlock()
		return false, nil // first time for this piece: fail
	}
	s.mu.Unlock()
	return s.inner.VerifyPiece(ctx, pieceIndex, expected)
}

func (s *FailNPieceStore) Move(ctx context.Context, target string, report piece_store.MoveProgressFunc) error {
	return s.inner.Move(ctx, target, report)
}
