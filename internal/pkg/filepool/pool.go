// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package filepool

import (
	"os"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"
)

// FilePool keeps at most one live fd per cacheKey. Active handles (ref>0) are
// pinned in the active map. When Release makes ref==0, the handle moves to the
// idle LRU and may be closed on eviction. This avoids reopening a file that is
// already open while still closing unused fds.
type FilePool struct {
	idle   *expirable.LRU[cacheKey, *File]
	active map[cacheKey]*File
	mu     sync.Mutex
}

func New() *FilePool {
	return &FilePool{
		idle:   expirable.NewLRU[cacheKey, *File](5000, onEvict, time.Minute*5),
		active: make(map[cacheKey]*File, 64),
	}
}

func onEvict(key cacheKey, value *File) {
	if value == nil {
		return
	}
	if value.ref.Load() > 0 {
		return
	}
	log.Debug().Str("path", key.path).Msg("close file")
	_ = value.File.Close()
	value.pool = nil
}

type cacheKey struct {
	path string
	flag int
	perm os.FileMode
	ttl  time.Duration
}

// Open returns a handle and bumps its ref. It prefers an active handle, then an
// idle one, and only opens a new fd if none exist.
func (pool *FilePool) Open(path string, flag int, perm os.FileMode, ttl time.Duration) (*File, error) {
	key := cacheKey{path: path, flag: flag, perm: perm, ttl: ttl}

	pool.mu.Lock()
	if f, ok := pool.active[key]; ok {
		f.ref.Add(1)
		pool.mu.Unlock()
		return f, nil
	}
	if f, ok := pool.idle.Get(key); ok {
		f.ref.Store(1)
		pool.active[key] = f
		pool.idle.Remove(key)
		pool.mu.Unlock()
		return f, nil
	}
	pool.mu.Unlock()

	fd, err := os.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}

	f := &File{File: fd, key: key, pool: pool}
	f.ref.Store(1)

	pool.mu.Lock()
	pool.active[key] = f
	pool.mu.Unlock()

	return f, nil
}

// File wraps an *os.File with ref counting.
type File struct {
	File *os.File
	pool *FilePool
	key  cacheKey
	ref  atomic.Int32
}

// Release decrements the ref. When it reaches zero, the fd moves to the idle
// cache and may be closed later on eviction.
func (f *File) Release() {
	if f == nil || f.pool == nil {
		return
	}

	if f.ref.Add(-1) != 0 {
		return
	}

	p := f.pool
	p.mu.Lock()
	delete(p.active, f.key)
	// Only idle handles live in LRU; eviction will close them.
	p.idle.Add(f.key, f)
	p.mu.Unlock()
}

// Close forcibly closes and removes the fd from both active and idle caches.
func (f *File) Close() {
	if f == nil || f.pool == nil {
		return
	}

	p := f.pool
	p.mu.Lock()
	delete(p.active, f.key)
	p.idle.Remove(f.key)
	p.mu.Unlock()

	f.ref.Store(0)
	_ = f.File.Close()
	f.pool = nil
}
