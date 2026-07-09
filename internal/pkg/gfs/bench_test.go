// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package gfs

import (
	"context"
	"os"
	"sync"
	"testing"
)

const benchFileSize = 64 << 20 // 64 MiB

// ---------------------------------------------------------------------------
// Read benchmarks
// ---------------------------------------------------------------------------

func benchReadUring(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f, err := os.CreateTemp("", "gfs_bench")
	if err != nil {
		b.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}

	ioc := NewIOContext()
	defer ioc.Close()
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

	f, err := os.CreateTemp("", "gfs_bench")
	if err != nil {
		b.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}

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

func BenchmarkReadUring4K(b *testing.B)   { benchReadUring(b, 4096) }
func BenchmarkReadPlain4K(b *testing.B)   { benchReadPlain(b, 4096) }
func BenchmarkReadUring16K(b *testing.B)  { benchReadUring(b, 16*1024) }
func BenchmarkReadPlain16K(b *testing.B)  { benchReadPlain(b, 16*1024) }
func BenchmarkReadUring64K(b *testing.B)  { benchReadUring(b, 64*1024) }
func BenchmarkReadPlain64K(b *testing.B)  { benchReadPlain(b, 64*1024) }
func BenchmarkReadUring256K(b *testing.B) { benchReadUring(b, 256*1024) }
func BenchmarkReadPlain256K(b *testing.B) { benchReadPlain(b, 256*1024) }

// ---------------------------------------------------------------------------
// Write benchmarks
// ---------------------------------------------------------------------------

func benchWriteUring(b *testing.B, bufSize int64) {
	b.Helper()
	b.StopTimer()

	f, err := os.CreateTemp("", "gfs_bench")
	if err != nil {
		b.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}

	ioc := NewIOContext()
	defer ioc.Close()
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

	f, err := os.CreateTemp("", "gfs_bench")
	if err != nil {
		b.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if err := f.Truncate(benchFileSize); err != nil {
		b.Fatal(err)
	}

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

func BenchmarkWriteUring4K(b *testing.B)   { benchWriteUring(b, 4096) }
func BenchmarkWritePlain4K(b *testing.B)   { benchWritePlain(b, 4096) }
func BenchmarkWriteUring16K(b *testing.B)  { benchWriteUring(b, 16*1024) }
func BenchmarkWritePlain16K(b *testing.B)  { benchWritePlain(b, 16*1024) }
func BenchmarkWriteUring64K(b *testing.B)  { benchWriteUring(b, 64*1024) }
func BenchmarkWritePlain64K(b *testing.B)  { benchWritePlain(b, 64*1024) }
func BenchmarkWriteUring256K(b *testing.B) { benchWriteUring(b, 256*1024) }
func BenchmarkWritePlain256K(b *testing.B) { benchWritePlain(b, 256*1024) }

// ---------------------------------------------------------------------------
// Concurrency stress
// ---------------------------------------------------------------------------

func TestIOContextConcurrency(t *testing.T) {
	f, err := os.CreateTemp("", "gfs_concurrency")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
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

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			buf := make([]byte, bufSize)
			for i := 0; i < opsPerWorker; i++ {
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
