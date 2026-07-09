// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package gfs

import (
	"context"
	"os"
)

// IOContext is a no-op on non-Linux platforms.
type IOContext struct{}

// NewIOContext returns a no-op IOContext.
func NewIOContext() *IOContext { return &IOContext{} }

// ReadAtCtx reads from f at off. ctx cancellation is not supported on this platform.
func ReadAtCtx(ctx context.Context, _ *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.ReadAt(p, off)
}

// WriteAtCtx writes to f at off. ctx cancellation is not supported on this platform.
func WriteAtCtx(ctx context.Context, _ *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.WriteAt(p, off)
}
