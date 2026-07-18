// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux || !neptune_uring

package gfs

import (
	"context"
	"fmt"
	"sync"

	"neptune/internal/pkg/disk_io"
)

type portableBackend struct {
	inflight sync.WaitGroup
	mu       sync.Mutex
	closed   bool
}

func newBackend() ioBackend {
	return &portableBackend{}
}

func (b *portableBackend) Execute(ctx context.Context, operation disk_io.Operation) disk_io.Result {
	if err := ctx.Err(); err != nil {
		return disk_io.Result{Err: err}
	}
	if !b.beginIO() {
		return disk_io.Result{Err: ErrIOContextClosed}
	}
	defer b.inflight.Done()

	switch op := operation.(type) {
	case disk_io.PRead:
		n, err := op.File.ReadAt(op.Buffer, op.Offset)
		return disk_io.Result{N: n, Err: err}
	case disk_io.PWrite:
		n, err := op.File.WriteAt(op.Buffer, op.Offset)
		return disk_io.Result{N: n, Err: err}
	default:
		return disk_io.Result{Err: fmt.Errorf("unsupported disk IO operation %T", operation)}
	}
}

func (b *portableBackend) beginIO() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.inflight.Add(1)
	return true
}

func (b *portableBackend) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.inflight.Wait()
}
