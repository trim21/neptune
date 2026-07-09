// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package gfs

import (
	"context"
	"os"
	"sync"
	"unsafe"

	"neptune/internal/pkg/sys"
	"neptune/internal/pkg/uring"
)

const ringEntries = 256

// minKernelMajor/Minor — only use io_uring on kernel >= 6.12.
const minKernelMajor, minKernelMinor = 6, 12

// IOContext holds a single io_uring ring and manages completion dispatch.
// All file I/O for one download session shares this ring.
// SQE user_data carries the completion channel pointer — no pending map needed.
type IOContext struct {
	ring  *uring.Ring
	start sync.Once
	stop  chan struct{}

	submitMu sync.Mutex // serializes QueueSQE + Submit
	chPool   sync.Pool
}

type ioResult struct {
	n   int32
	err error
}

// NewIOContext creates an IOContext backed by a shared io_uring ring.
// Returns a ringless fallback context on kernels < 6.18 or if io_uring
// setup fails.
func NewIOContext() *IOContext {
	major, minor := sys.KernelVersion()
	if major < minKernelMajor || (major == minKernelMajor && minor < minKernelMinor) {
		return &IOContext{}
	}
	r, err := uring.New(ringEntries)
	if err != nil {
		return &IOContext{}
	}
	ioc := &IOContext{
		ring: r,
		stop: make(chan struct{}),
	}
	ioc.chPool.New = func() any { return make(chan ioResult, 1) }
	return ioc
}

func (ioc *IOContext) startPoller() {
	ioc.start.Do(func() {
		go ioc.pollLoop()
	})
}

func (ioc *IOContext) pollLoop() {
	for {
		select {
		case <-ioc.stop:
			return
		default:
		}

		cqe, err := ioc.ring.WaitCQE()
		if err != nil {
			return
		}

		// user_data is the channel pointer — unpack and dispatch.
		ch := *(*chan ioResult)(unsafe.Pointer(&cqe.UserData))
		ch <- ioResult{n: cqe.Res, err: cqe.Error()}
		ioc.ring.SeenCQE(cqe)
	}
}

// submitAndWait submits an SQE and waits for completion, respecting ctx.
func (ioc *IOContext) submitAndWait(ctx context.Context, op uring.ReadWriteOp) (int, error) {
	ioc.startPoller()

	ch := ioc.chPool.Get().(chan ioResult)
	// Embed the channel pointer in user_data so the poller can dispatch
	// without a map lookup.
	ud := *(*uint64)(unsafe.Pointer(&ch))

	ioc.submitMu.Lock()
	if err := ioc.ring.QueueSQE(op, 0, ud); err != nil {
		ioc.submitMu.Unlock()
		ioc.chPool.Put(ch)
		return 0, err
	}
	_, err := ioc.ring.Submit()
	ioc.submitMu.Unlock()
	if err != nil {
		ioc.chPool.Put(ch)
		return 0, err
	}

	select {
	case <-ctx.Done():
		// Drain channel (poller may have already dispatched).
		// Don't return ch to pool — poller may still reference it.
		select {
		case <-ch:
		default:
		}
		return 0, ctx.Err()
	case r := <-ch:
		ioc.chPool.Put(ch)
		if r.err != nil {
			return 0, r.err
		}
		return int(r.n), nil
	}
}

// Close shuts down the poll goroutine and closes the ring.
func (ioc *IOContext) Close() {
	if ioc.ring != nil {
		close(ioc.stop)
		ioc.ring.Close()
	}
}

// ReadAtCtx reads len(p) bytes from f at off, respecting ctx cancellation.
func ReadAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if ioc.ring == nil {
		return fallbackReadAt(ctx, f, p, off)
	}
	return ioc.submitAndWait(ctx, uring.Read(f.Fd(), p, uint64(off)))
}

// WriteAtCtx writes len(p) bytes to f at off, respecting ctx cancellation.
func WriteAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if ioc.ring == nil {
		return fallbackWriteAt(ctx, f, p, off)
	}
	return ioc.submitAndWait(ctx, uring.Write(f.Fd(), p, uint64(off)))
}

func fallbackReadAt(ctx context.Context, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.ReadAt(p, off)
}

func fallbackWriteAt(ctx context.Context, f *os.File, p []byte, off int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return f.WriteAt(p, off)
}
