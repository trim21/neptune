// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux || !neptune_uring

package gfs

import (
	"context"
	"os"
)

// IOContext is a no-op when the io_uring backend is disabled.
type IOContext struct{}

// NewIOContext returns a no-op I/O context.
func NewIOContext() *IOContext { return &IOContext{} }

// Close is a no-op for the fallback implementation.
func (*IOContext) Close() {}

// ReadAtCtx reads from f at off. Cancellation after submission is not supported.
func ReadAtCtx(ctx context.Context, _ *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.ReadAt(p, off)
}

// WriteAtCtx writes to f at off. Cancellation after submission is not supported.
func WriteAtCtx(ctx context.Context, _ *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.WriteAt(p, off)
}
