// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_store

import (
	"context"
	"crypto/sha1"
	"io"
	"os"
	"path/filepath"
	"time"

	"neptune/internal/pkg/fadvise"
	"neptune/internal/pkg/fallocate"
	"neptune/internal/pkg/mempool"
)

const verifyReadSize = 1 << 20

var verifyBufferPool mempool.Pool

func (s *FileStore) filePath(fileIndex int) string {
	return filepath.Join(s.basePath, s.info.Files[fileIndex].Path)
}

func (s *FileStore) WriteChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) error {
	s.opMu.RLock()
	defer s.opMu.RUnlock()

	offset := int64(pieceIndex)*s.info.PieceLength + int64(begin)
	size := int64(len(data))
	var off int64
	for chunk := range s.info.FileChunks(offset, offset+size) {
		path := s.filePath(chunk.FileIndex)
		f, fresh, err := s.fp.Open(path, os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour)
		if err != nil {
			return err
		}
		if fresh {
			_ = fadvise.Random(f.File, 0, 0)
			if s.fallocate && s.selectedFilesSet.Contains(uint32(chunk.FileIndex)) {
				if !s.fallocatedBm.Contains(uint32(chunk.FileIndex)) {
					_ = fallocate.Fallocate(f.File, 0, s.info.Files[chunk.FileIndex].Length)
					s.fallocatedBm.Set(uint32(chunk.FileIndex))
				}
			}
		}
		_, err = s.diskIO.WriteAtCtx(ctx, f.File, data[off:off+chunk.Length], chunk.OffsetOfFile)
		if err != nil {
			f.Close()
			return err
		}
		f.Release()
		off += chunk.Length
	}
	return nil
}

func (s *FileStore) ReadChunk(ctx context.Context, pieceIndex uint32, begin uint32, data []byte) (int, error) {
	s.opMu.RLock()
	defer s.opMu.RUnlock()

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
		rn, err := s.diskIO.ReadAtCtx(ctx, f.File, data[n:n+int(chunk.Length)], chunk.OffsetOfFile)
		n += rn
		f.Release()
		// A short read means the file is truncated or smaller than the
		// torrent metadata declares. ReadAt reports this as io.EOF on the
		// portable backend, while io_uring pread returns 0 with no error;
		// normalize both to ErrUnexpectedEOF so callers treat it as data
		// corruption instead of a clean EOF (or silent zero fill).
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		if rn < int(chunk.Length) && err == nil {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// VerifyPiece reads piece data from disk, computes SHA1, and compares.
func (s *FileStore) VerifyPiece(ctx context.Context, pieceIndex uint32, expected [sha1.Size]byte) (bool, error) {
	s.opMu.RLock()
	defer s.opMu.RUnlock()

	hasher := sha1.New()
	buf := mempool.GetWithCapFromPool(&verifyBufferPool, verifyReadSize)
	defer verifyBufferPool.Put(buf)

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
			toRead := min(left, int64(len(buf.B)))
			n, err := s.diskIO.ReadAtCtx(ctx, f.File, buf.B[:toRead], fileOff)
			if n > 0 {
				hasher.Write(buf.B[:n])
				fileOff += int64(n)
				left -= int64(n)
			}
			// ReadAt reports EOF before the piece range is satisfied; the
			// io_uring backend instead returns 0 with a nil error. Both mean
			// the file is truncated — without the n==0 branch VerifyPiece
			// would spin forever on the uring backend.
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			if err != nil {
				f.Release()
				return false, err
			}
			if n == 0 {
				f.Release()
				return false, io.ErrUnexpectedEOF
			}
		}
		f.Release()
	}

	var digest [sha1.Size]byte
	copy(digest[:], hasher.Sum(nil))
	return digest == expected, nil
}
