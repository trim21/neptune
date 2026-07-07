// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_store

import (
	"crypto/sha1"
	"os"
	"path/filepath"
	"time"

	"neptune/internal/pkg/fadvise"
)

func (s *FileStore) filePath(fileIndex int) string {
	return filepath.Join(s.basePath, s.info.Files[fileIndex].Path)
}

func (s *FileStore) WriteChunk(pieceIndex uint32, begin uint32, data []byte) error {
	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	size := int64(len(data))
	var off int64
	for chunk := range s.info.FileChunks(offset, offset+size) {
		f, fresh, err := s.fp.Open(
			s.filePath(chunk.FileIndex),
			os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour,
		)
		if err != nil {
			return err
		}
		if fresh {
			_ = fadvise.Random(f.File, 0, 0)
		}
		_, err = f.File.WriteAt(data[off:off+chunk.Length], chunk.OffsetOfFile)
		if err != nil {
			f.Close()
			return err
		}
		f.Release()
		off += chunk.Length
	}
	return nil
}

func (s *FileStore) ReadChunk(pieceIndex uint32, begin uint32, data []byte) (int, error) {
	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	size := int64(len(data))
	var n int
	for chunk := range s.info.FileChunks(offset, offset+size) {
		f, fresh, err := s.fp.Open(s.filePath(chunk.FileIndex), os.O_RDONLY, 0, time.Hour)
		if err != nil {
			return n, err
		}
		if fresh {
			_ = fadvise.Random(f.File, 0, 0)
		}
		rn, err := f.File.ReadAt(data[n:n+int(chunk.Length)], chunk.OffsetOfFile)
		n += rn
		f.Release()
		if err != nil || rn < int(chunk.Length) {
			return n, err
		}
	}
	return n, nil
}

// VerifyPiece reads piece data from disk, computes SHA1, and compares.
func (s *FileStore) VerifyPiece(pieceIndex uint32, expected [sha1.Size]byte) (bool, error) {
	hasher := sha1.New()
	var buf [16 * 1024]byte

	for chunk := range s.info.PieceFileChunks(pieceIndex) {
		f, fresh, err := s.fp.Open(s.filePath(chunk.FileIndex), os.O_RDONLY, 0, time.Hour)
		if err != nil {
			return false, err
		}
		if fresh {
			_ = fadvise.Random(f.File, 0, 0)
		}

		fileOff := chunk.OffsetOfFile
		left := chunk.Length
		for left > 0 {
			toRead := min(left, int64(len(buf)))
			n, err := f.File.ReadAt(buf[:toRead], fileOff)
			if n > 0 {
				hasher.Write(buf[:n])
				fileOff += int64(n)
				left -= int64(n)
			}
			if err != nil {
				if n == 0 {
					f.Release()
					return false, err
				}
				break
			}
		}
		f.Release()
	}

	var digest [sha1.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest == expected, nil
}
