// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package piece_store

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/gfs"
)

// TestReadChunkTruncatedFileIsUnexpectedEOF is a regression test: a file
// smaller than the torrent metadata declares used to surface a bare io.EOF
// from ReadChunk (crashing the process via setError's panic), and on the
// io_uring backend pread returns 0 with no error at all, silently filling
// the caller's buffer with stale pool data. Both must become
// io.ErrUnexpectedEOF.
func TestReadChunkTruncatedFileIsUnexpectedEOF(t *testing.T) {
	info := moveTestInfo([]meta.File{{Path: "data", Length: 32 * 1024}})
	info.Name = "truncated"

	basePath := t.TempDir()
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		t.Fatal(err)
	}
	// File on disk is only half of the declared size.
	if err := os.WriteFile(filepath.Join(basePath, "data"), make([]byte, 16*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	ioc := gfs.NewIOContext()
	t.Cleanup(ioc.Close)
	selected := bm.New(uint32(len(info.Files)))
	selected.Fill()

	s := NewFileStore(info, basePath, filepool.New(), ioc, selected, false)
	t.Cleanup(func() {
		s.fp.InvalidatePaths([]string{s.filePath(0)})
	})

	buf := make([]byte, 32*1024)
	n, err := s.ReadChunk(context.Background(), 0, 0, buf)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ReadChunk short read: err = %v, want io.ErrUnexpectedEOF (n=%d)", err, n)
	}
}

// TestVerifyPieceTruncatedFileErrors is a regression test: on the io_uring
// backend pread returns (0, nil) at EOF, which used to make VerifyPiece
// loop forever (left never decreased). It must return an error instead.
func TestVerifyPieceTruncatedFileErrors(t *testing.T) {
	info := moveTestInfo([]meta.File{{Path: "data", Length: 32 * 1024}})
	info.Name = "truncated"

	basePath := t.TempDir()
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(basePath, "data"), make([]byte, 16*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	ioc := gfs.NewIOContext()
	t.Cleanup(ioc.Close)
	selected := bm.New(uint32(len(info.Files)))
	selected.Fill()

	s := NewFileStore(info, basePath, filepool.New(), ioc, selected, false)
	t.Cleanup(func() {
		s.fp.InvalidatePaths([]string{s.filePath(0)})
	})

	ok, err := s.VerifyPiece(context.Background(), 0, [20]byte{})
	if err == nil {
		t.Fatalf("VerifyPiece on truncated file: err = nil (ok=%v), want error", ok)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("VerifyPiece on truncated file: err = %v, want io.ErrUnexpectedEOF", err)
	}
}
