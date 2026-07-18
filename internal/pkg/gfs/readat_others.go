// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux || !neptune_uring

package gfs

import (
	"context"
	"os"
)

// IOContext uses the portable file backend with the shared disk scheduler.
type IOContext struct {
	scheduler ioScheduler
}

// NewIOContext creates a context backed by portable file operations.
func NewIOContext() *IOContext { return &IOContext{scheduler: newIOScheduler()} }

func (ioc *IOContext) Close() { ioc.scheduler.close() }

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
