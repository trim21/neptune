// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package gfs

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"neptune/internal/pkg/diskio"
)

func TestPathIOIsOwnedByIOContext(t *testing.T) {
	ioc := NewIOContext()
	dir := t.TempDir()
	pathIO := ioc.ForPath(dir)
	f, err := os.CreateTemp(dir, "io-context-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if _, err = pathIO.WriteAtCtx(context.Background(), f, []byte("data"), 0); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err = pathIO.ReadAtCtx(context.Background(), f, buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if string(buf) != "data" {
		t.Fatalf("read data = %q", buf)
	}

	ioc.Close()
	if _, err = pathIO.ReadAtCtx(context.Background(), f, buf, 0); !errors.Is(err, diskio.ErrClosed) {
		t.Fatalf("operation after IOContext.Close error = %v, want %v", err, diskio.ErrClosed)
	}
}
