// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package filepool

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentOpenUsesOneCanonicalFile(t *testing.T) {
	pool := New()
	path := filepath.Join(t.TempDir(), "data")

	realOpen := pool.openFile
	openStarted := make(chan struct{})
	continueOpen := make(chan struct{})
	var openCalls atomic.Int32
	pool.openFile = func(path string, flag int, perm os.FileMode) (*os.File, error) {
		if openCalls.Add(1) == 1 {
			close(openStarted)
		}
		<-continueOpen
		return realOpen(path, flag, perm)
	}

	const workers = 32
	type openResult struct {
		file  *File
		err   error
		fresh bool
	}

	start := make(chan struct{})
	results := make(chan openResult, workers)
	var ready sync.WaitGroup
	var finished sync.WaitGroup
	ready.Add(workers)
	finished.Add(workers)
	for range workers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			f, fresh, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
			results <- openResult{file: f, fresh: fresh, err: err}
			if err == nil {
				f.Release()
			}
		}()
	}

	ready.Wait()
	close(start)
	<-openStarted
	close(continueOpen)

	var canonical *File
	freshCount := 0
	for range workers {
		result := <-results
		if result.err != nil {
			t.Fatalf("Open() error = %v", result.err)
		}
		if canonical == nil {
			canonical = result.file
		} else if result.file != canonical {
			t.Fatalf("Open() returned different files: %p and %p", canonical, result.file)
		}
		if result.fresh {
			freshCount++
		}
	}
	finished.Wait()
	defer canonical.Close()

	if got := openCalls.Load(); got != 1 {
		t.Fatalf("os.OpenFile calls = %d, want 1", got)
	}
	if freshCount != 1 {
		t.Fatalf("fresh results = %d, want 1", freshCount)
	}

	pool.mu.Lock()
	refs := canonical.refs
	idle := canonical.idleElem != nil
	pool.mu.Unlock()
	if refs != 0 {
		t.Fatalf("refs after Release = %d, want 0", refs)
	}
	if !idle {
		t.Fatal("released file is not present in idle LRU")
	}
}

func TestConcurrentOpenReleaseKeepsActiveFileOutOfIdle(t *testing.T) {
	pool := New()
	path := filepath.Join(t.TempDir(), "data")
	canonical, _, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.Close()
	canonical.Release()

	const (
		workers    = 32
		iterations = 500
	)
	start := make(chan struct{})
	errs := make(chan string, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			<-start
			for range iterations {
				f, fresh, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
				if err != nil {
					errs <- err.Error()
					return
				}
				if fresh || f != canonical {
					errs <- "Open returned a non-canonical file"
					f.Release()
					return
				}
				runtime.Gosched()
				f.Release()
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	pool.mu.Lock()
	got := pool.files[canonical.key]
	refs := canonical.refs
	idle := canonical.idleElem != nil
	pool.mu.Unlock()
	if got != canonical {
		t.Fatalf("canonical entry = %p, want %p", got, canonical)
	}
	if refs != 0 {
		t.Fatalf("refs = %d, want 0", refs)
	}
	if !idle {
		t.Fatal("final file is not idle")
	}
}

func TestCloseWaitsForRemainingReferences(t *testing.T) {
	pool := New()
	path := filepath.Join(t.TempDir(), "data")
	canonical, _, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	canonical.Release()

	first, _, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, _, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Open did not share the canonical file")
	}

	first.Close()
	if _, err = second.File.Stat(); err != nil {
		t.Fatalf("Close closed a file with another active reference: %v", err)
	}

	replacement, fresh, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if !fresh {
		t.Fatal("replacement file was not freshly opened")
	}
	if replacement == first {
		t.Fatal("Open reused an invalidated file")
	}

	second.Release()
	if _, err := first.File.Stat(); err == nil {
		t.Fatal("invalidated file remained open after its final reference was released")
	}
	replacement.Release()
}

func TestExpiredIdleFileIsClosedBeforeReplacement(t *testing.T) {
	pool := New()
	now := time.Unix(1_700_000_000, 0)
	pool.now = func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "data")

	old, _, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	now = now.Add(2 * time.Minute)

	replacement, fresh, err := pool.Open(path, os.O_RDWR|os.O_CREATE, 0600, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if !fresh {
		t.Fatal("expired file was reused")
	}
	if replacement == old {
		t.Fatal("expired file remained canonical")
	}
	if _, err := old.File.Stat(); err == nil {
		t.Fatal("expired file descriptor was not closed")
	}

	replacement.Release()
}
