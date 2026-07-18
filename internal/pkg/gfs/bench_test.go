// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux && neptune_uring

package gfs

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

const benchFileSize = 64 << 20 // 64 MiB.

var benchBufferSizes = []struct {
	name string
	size int64
}{
	{name: "16K", size: 16 << 10},
	{name: "32K", size: 32 << 10},
	{name: "64K", size: 64 << 10},
	{name: "128K", size: 128 << 10},
	{name: "256K", size: 256 << 10},
	{name: "512K", size: 512 << 10},
}

func newBenchFile(b *testing.B) *os.File {
	b.Helper()
	f, err := os.CreateTemp(b.TempDir(), "gfs_bench")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.Remove(f.Name()) })
	return f
}

// ---------------------------------------------------------------------------
// Read benchmarks.
// ---------------------------------------------------------------------------

func benchReadUring(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f := newBenchFile(b)
	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	ioc := NewIOContext()
	defer ioc.Close()
	if ioc.backend.(*uringBackend).ring == nil {
		b.Skip("io_uring is unavailable")
	}
	ctx := context.Background()

	b.SetBytes(bufSize)
	b.ResetTimer()
	b.StartTimer()

	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, bufSize)
		var off int64
		for pb.Next() {
			off = (off + bufSize) % benchFileSize
			if _, err := ReadAtCtx(ctx, ioc, f, buf, off); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchReadPlain(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f := newBenchFile(b)
	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	b.SetBytes(bufSize)
	b.ResetTimer()
	b.StartTimer()

	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, bufSize)
		var off int64
		for pb.Next() {
			off = (off + bufSize) % benchFileSize
			if _, err := f.ReadAt(buf, off); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkReadAt(b *testing.B) {
	for _, size := range benchBufferSizes {
		b.Run(size.name, func(b *testing.B) {
			b.Run("io_uring", func(b *testing.B) {
				benchReadUring(b, size.size)
			})
			b.Run("pread", func(b *testing.B) {
				benchReadPlain(b, size.size)
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Write benchmarks.
// ---------------------------------------------------------------------------

func benchWriteUring(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f := newBenchFile(b)
	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	ioc := NewIOContext()
	defer ioc.Close()
	if ioc.backend.(*uringBackend).ring == nil {
		b.Skip("io_uring is unavailable")
	}
	ctx := context.Background()
	fill := make([]byte, bufSize)

	b.SetBytes(bufSize)
	b.ResetTimer()
	b.StartTimer()

	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, bufSize)
		copy(buf, fill)
		var off int64
		for pb.Next() {
			off = (off + bufSize) % benchFileSize
			if _, err := WriteAtCtx(ctx, ioc, f, buf, off); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchWritePlain(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f := newBenchFile(b)
	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	fill := make([]byte, bufSize)

	b.SetBytes(bufSize)
	b.ResetTimer()
	b.StartTimer()

	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, bufSize)
		copy(buf, fill)
		var off int64
		for pb.Next() {
			off = (off + bufSize) % benchFileSize
			if _, err := f.WriteAt(buf, off); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkWriteAt(b *testing.B) {
	for _, size := range benchBufferSizes {
		b.Run(size.name, func(b *testing.B) {
			b.Run("io_uring", func(b *testing.B) {
				benchWriteUring(b, size.size)
			})
			b.Run("pwrite", func(b *testing.B) {
				benchWritePlain(b, size.size)
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrency stress.
// ---------------------------------------------------------------------------

func TestIOContextConcurrency(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "gfs_concurrency")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const fileSize = 1 << 20
	if err := f.Truncate(fileSize); err != nil {
		t.Fatal(err)
	}

	ioc := NewIOContext()
	defer ioc.Close()
	ctx := context.Background()

	var wg sync.WaitGroup
	const workers, opsPerWorker, bufSize = 64, 100, 4096

	for w := range workers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			buf := make([]byte, bufSize)
			for i := range opsPerWorker {
				off := int64((id*opsPerWorker + i) * bufSize % fileSize)
				if _, err := ReadAtCtx(ctx, ioc, f, buf, off); err != nil {
					t.Errorf("worker %d: %v", id, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}

func TestIOContextCloseRejectsNewIO(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "gfs_close")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ioc := NewIOContext()
	ioc.Close()

	_, err = WriteAtCtx(context.Background(), ioc, f, []byte("x"), 0)
	if !errors.Is(err, ErrIOContextClosed) {
		t.Fatalf("WriteAtCtx error = %v, want %v", err, ErrIOContextClosed)
	}
}

func TestIOContextCloseWaitsForInflightIO(t *testing.T) {
	ioc := NewIOContext()
	backend := ioc.backend.(*uringBackend)
	if !backend.beginIO() {
		t.Fatal("failed to begin I/O")
	}
	ioEnded := false
	defer func() {
		if !ioEnded {
			backend.endIO()
		}
	}()

	closeDone := make(chan struct{})
	go func() {
		ioc.Close()
		close(closeDone)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		backend.submitMu.Lock()
		closed := backend.closed
		backend.submitMu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not start")
		}
		time.Sleep(time.Millisecond)
	}

	select {
	case <-closeDone:
		t.Fatal("Close returned with I/O still in flight")
	default:
	}

	backend.endIO()
	ioEnded = true
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after I/O completed")
	}
}
