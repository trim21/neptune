//go:build !release

package piece_store

import (
	"bytes"
	"crypto/sha1"
	"slices"
	"sync"

	"neptune/internal/meta"
)

// MemStore records writes in memory. Used by tests for zero-I/O fuzzing.
type MemStore struct {
	data map[int64][]byte
	info meta.Info
	mu   sync.RWMutex
}

// NewMemStore creates an in-memory store for testing.
func NewMemStore(info meta.Info) Store {
	return &MemStore{info: info, data: make(map[int64][]byte)}
}

func (s *MemStore) WriteChunk(pieceIndex uint32, begin uint32, data []byte) error {
	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	cp := make([]byte, len(data))
	copy(cp, data)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[offset] = cp
	return nil
}

func (s *MemStore) ReadChunk(pieceIndex uint32, begin uint32, data []byte) (int, error) {
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
func (s *MemStore) VerifyPiece(pieceIndex uint32, expected [sha1.Size]byte) (bool, error) {
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
