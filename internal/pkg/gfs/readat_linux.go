// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux && neptune_uring

package gfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"unsafe"

	"neptune/internal/pkg/diskio"
	"neptune/internal/pkg/sys"
	"neptune/internal/pkg/uring"
)

const ringEntries = 256

// minKernelMajor/Minor - only use io_uring on kernel >= 6.12.
const minKernelMajor, minKernelMinor = 6, 12

const closeUserData uint64 = 0

type uringBackend struct {
	chPool sync.Pool
	ring   *uring.Ring

	// submitMu serializes SQ access and gates request admission during Close.
	submitMu      sync.Mutex
	inflight      sync.WaitGroup
	closed        bool
	pollerStarted bool
	pollerDone    chan struct{}
}

type uringResult struct {
	err error
	n   int32
}

func newBackend() ioBackend {
	b := &uringBackend{pollerDone: make(chan struct{})}

	major, minor := sys.KernelVersion()
	if major < minKernelMajor || (major == minKernelMajor && minor < minKernelMinor) {
		return b
	}
	ring, err := uring.New(ringEntries)
	if err != nil {
		return b
	}
	b.ring = ring
	b.chPool.New = func() any { return make(chan uringResult, 1) }
	return b
}

func (b *uringBackend) Execute(ctx context.Context, operation diskio.Operation) diskio.Result {
	if err := ctx.Err(); err != nil {
		return diskio.Result{Err: err}
	}
	if !b.beginIO() {
		return diskio.Result{Err: ErrIOContextClosed}
	}
	defer b.endIO()

	switch op := operation.(type) {
	case diskio.PRead:
		if b.ring == nil {
			n, err := op.File.ReadAt(op.Buffer, op.Offset)
			return diskio.Result{N: n, Err: err}
		}
		n, err := b.submitAndWait(ctx, uring.Read(op.File.Fd(), op.Buffer, uint64(op.Offset)))
		return diskio.Result{N: n, Err: err}
	case diskio.PWrite:
		if b.ring == nil {
			n, err := op.File.WriteAt(op.Buffer, op.Offset)
			return diskio.Result{N: n, Err: err}
		}
		n, err := b.submitAndWait(ctx, uring.Write(op.File.Fd(), op.Buffer, uint64(op.Offset)))
		if err == nil && n != len(op.Buffer) {
			err = io.ErrShortWrite
		}
		return diskio.Result{N: n, Err: err}
	default:
		return diskio.Result{Err: fmt.Errorf("unsupported disk IO operation %T", operation)}
	}
}

// startPollerLocked starts the sole CQ consumer. submitMu must be held.
func (b *uringBackend) startPollerLocked() {
	if b.pollerStarted {
		return
	}
	b.pollerStarted = true
	go b.pollLoop()
}

func (b *uringBackend) pollLoop() {
	defer close(b.pollerDone)

	for {
		cqe, err := b.ring.WaitCQE()
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}

		if cqe.UserData == closeUserData {
			b.ring.SeenCQE(cqe)
			return
		}

		ch := *(*chan uringResult)(unsafe.Pointer(&cqe.UserData))
		ch <- uringResult{n: cqe.Res, err: cqe.Error()}
		b.ring.SeenCQE(cqe)
	}
}

// submitAndWait checks ctx before submitting, then waits for completion. A
// submitted operation is never cancelled by this context.
func (b *uringBackend) submitAndWait(ctx context.Context, op uring.ReadWriteOp) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	ch := b.chPool.Get().(chan uringResult)
	ud := *(*uint64)(unsafe.Pointer(&ch))

	b.submitMu.Lock()
	b.startPollerLocked()
	if err := b.ring.QueueSQE(op, 0, ud); err != nil {
		b.submitMu.Unlock()
		b.chPool.Put(ch)
		return 0, err
	}
	_, err := b.ring.Submit()
	b.submitMu.Unlock()
	if err != nil {
		b.chPool.Put(ch)
		return 0, err
	}

	result := <-ch
	b.chPool.Put(ch)
	if result.err != nil {
		return 0, result.err
	}
	return int(result.n), nil
}

func (b *uringBackend) beginIO() bool {
	b.submitMu.Lock()
	defer b.submitMu.Unlock()
	if b.closed {
		return false
	}
	b.inflight.Add(1)
	return true
}

func (b *uringBackend) endIO() {
	b.inflight.Done()
}

func (b *uringBackend) Close() {
	b.submitMu.Lock()
	b.closed = true
	b.submitMu.Unlock()
	b.inflight.Wait()

	if b.ring == nil {
		return
	}

	b.submitMu.Lock()
	pollerStarted := b.pollerStarted
	b.submitMu.Unlock()
	if !pollerStarted {
		_ = b.ring.Close()
		return
	}

	b.submitMu.Lock()
	err := b.ring.QueueSQE(uring.Nop(), 0, closeUserData)
	if err == nil {
		_, err = b.ring.Submit()
	}
	b.submitMu.Unlock()
	if err != nil {
		return
	}

	<-b.pollerDone
	_ = b.ring.Close()
}
