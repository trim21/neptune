// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gfs

import (
	"context"
	"errors"
	"os"
	"sync"

	"neptune/internal/pkg/diskio"
)

// ErrIOContextClosed is returned when IO is attempted after Close begins.
var ErrIOContextClosed = errors.New("io context closed")

type ioBackend interface {
	diskio.Executor
	Close()
}

// IOContext owns the file IO backend and its device scheduler.
type IOContext struct {
	backend   ioBackend
	scheduler ioScheduler
	closeOnce sync.Once
}

func NewIOContext() *IOContext {
	backend := newBackend()
	return &IOContext{
		backend:   backend,
		scheduler: newIOScheduler(backend),
	}
}

// Close drains scheduled operations before closing the execution backend.
func (ioc *IOContext) Close() {
	ioc.closeOnce.Do(func() {
		ioc.scheduler.close()
		ioc.backend.Close()
	})
}

// ReadAtCtx executes a positional read directly on the backend.
func ReadAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	result := ioc.backend.Execute(ctx, diskio.PRead{File: f, Buffer: p, Offset: off})
	return result.N, result.Err
}

// WriteAtCtx executes a positional write directly on the backend.
func WriteAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	result := ioc.backend.Execute(ctx, diskio.PWrite{File: f, Buffer: p, Offset: off})
	return result.N, result.Err
}
