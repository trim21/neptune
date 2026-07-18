// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux && neptune_uring

package gfs

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"neptune/internal/pkg/sys"
	"neptune/internal/pkg/uring"
)

const ringEntries = 256

// minKernelMajor/Minor — only use io_uring on kernel >= 6.12.
const minKernelMajor, minKernelMinor = 6, 12

const closeUserData uint64 = 0

// ErrIOContextClosed is returned when I/O is attempted after Close begins.
var ErrIOContextClosed = errors.New("io context closed")

// IOContext holds a single io_uring ring and manages completion dispatch.
// All file I/O for one download session shares this ring.
// SQE user_data carries the completion channel pointer — no pending map needed.
type IOContext struct {
	chPool    sync.Pool
	ring      *uring.Ring
	scheduler ioScheduler

	// submitMu serializes SQ access and gates request admission during Close.
	submitMu      sync.Mutex
	inflight      sync.WaitGroup
	closed        bool
	pollerStarted bool
	pollerDone    chan struct{}
	closeOnce     sync.Once
}

type ioResult struct {
	err error
	n   int32
}

// NewIOContext creates an IOContext backed by a shared io_uring ring.
// Returns a ringless fallback context on kernels < 6.12 or if io_uring
// setup fails.
func NewIOContext() *IOContext {
	ioc := &IOContext{
		pollerDone: make(chan struct{}),
		scheduler:  newIOScheduler(),
	}

	major, minor := sys.KernelVersion()
	if major < minKernelMajor || (major == minKernelMajor && minor < minKernelMinor) {
		return ioc
	}
	r, err := uring.New(ringEntries)
	if err != nil {
		return ioc
	}
	ioc.ring = r
	ioc.chPool.New = func() any { return make(chan ioResult, 1) }
	return ioc
}

// startPollerLocked starts the sole CQ consumer. submitMu must be held.
func (ioc *IOContext) startPollerLocked() {
	if ioc.pollerStarted {
		return
	}
	ioc.pollerStarted = true
	go ioc.pollLoop()
}

func (ioc *IOContext) pollLoop() {
	defer close(ioc.pollerDone)

	for {
		cqe, err := ioc.ring.WaitCQE()
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return
		}

		if cqe.UserData == closeUserData {
			ioc.ring.SeenCQE(cqe)
			return
		}

		// user_data is the channel pointer — unpack and dispatch.
		ch := *(*chan ioResult)(unsafe.Pointer(&cqe.UserData))
		ch <- ioResult{n: cqe.Res, err: cqe.Error()}
		ioc.ring.SeenCQE(cqe)
	}
}

// submitAndWait checks ctx before submitting, then waits for the submitted I/O
// to complete. A submitted operation is never cancelled by this context.
func (ioc *IOContext) submitAndWait(ctx context.Context, op uring.ReadWriteOp) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	ch := ioc.chPool.Get().(chan ioResult)
	// Embed the channel pointer in user_data so the poller can dispatch
	// without a map lookup.
	ud := *(*uint64)(unsafe.Pointer(&ch))

	ioc.submitMu.Lock()
	ioc.startPollerLocked()
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

	r := <-ch
	ioc.chPool.Put(ch)
	if r.err != nil {
		return 0, r.err
	}
	return int(r.n), nil
}

// Close rejects new I/O and waits for every accepted read and write to complete.
// It stops the poller before unmapping the ring and is safe to call concurrently.
func (ioc *IOContext) Close() {
	ioc.closeOnce.Do(ioc.close)
}

func (ioc *IOContext) close() {
	ioc.scheduler.close()

	ioc.submitMu.Lock()
	ioc.closed = true
	ioc.submitMu.Unlock()

	ioc.inflight.Wait()

	if ioc.ring == nil {
		return
	}

	ioc.submitMu.Lock()
	pollerStarted := ioc.pollerStarted
	ioc.submitMu.Unlock()
	if !pollerStarted {
		_ = ioc.ring.Close()
		return
	}

	// The poller is now blocked in WaitCQE with no data I/O left. Submit a NOP
	// completion to wake it, then wait for it to stop before Ring.Close unmaps
	// the CQ ring.
	ioc.submitMu.Lock()
	err := ioc.ring.QueueSQE(uring.Nop(), 0, closeUserData)
	if err == nil {
		_, err = ioc.ring.Submit()
	}
	ioc.submitMu.Unlock()
	if err != nil {
		return
	}

	<-ioc.pollerDone
	_ = ioc.ring.Close()
}

// ReadAtCtx checks ctx before submitting, then reads len(p) bytes from f at off.
func ReadAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if !ioc.beginIO() {
		return 0, ErrIOContextClosed
	}
	defer ioc.endIO()

	if ioc.ring == nil {
		return fallbackReadAt(ctx, f, p, off)
	}
	return ioc.submitAndWait(ctx, uring.Read(f.Fd(), p, uint64(off)))
}

// WriteAtCtx checks ctx before submitting, then writes len(p) bytes to f at off.
func WriteAtCtx(ctx context.Context, ioc *IOContext, f *os.File, p []byte, off int64) (int, error) {
	if !ioc.beginIO() {
		return 0, ErrIOContextClosed
	}
	defer ioc.endIO()

	if ioc.ring == nil {
		return fallbackWriteAt(ctx, f, p, off)
	}

	n, err := ioc.submitAndWait(ctx, uring.Write(f.Fd(), p, uint64(off)))
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (ioc *IOContext) beginIO() bool {
	ioc.submitMu.Lock()
	defer ioc.submitMu.Unlock()
	if ioc.closed {
		return false
	}
	ioc.inflight.Add(1)
	return true
}

func (ioc *IOContext) endIO() {
	ioc.inflight.Done()
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
