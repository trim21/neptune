// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package filepool

import (
	"container/list"
	"os"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const maxIdleFiles = 5000

type openCall struct {
	done chan struct{}
	err  error
}

// FilePool keeps one canonical fd per cacheKey. Every file state transition is
// protected by mu; the actual open and close syscalls happen without the lock.
// Idle expiration is processed lazily by subsequent pool operations.
type FilePool struct {
	// nextExpiry may refer to an entry that has since become active. In that
	// case the next maintenance pass simply recomputes the actual minimum.
	nextExpiry time.Time
	openFile   func(string, int, os.FileMode) (*os.File, error)
	now        func() time.Time
	files      map[cacheKey]*File
	opening    map[cacheKey]*openCall
	idle       list.List
	mu         sync.Mutex
}

func New() *FilePool {
	return &FilePool{
		openFile: os.OpenFile,
		now:      time.Now,
		files:    make(map[cacheKey]*File, 64),
		opening:  make(map[cacheKey]*openCall),
	}
}

type cacheKey struct {
	path string
	flag int
	perm os.FileMode
	ttl  time.Duration
}

// Open returns a handle and increments its reference count. Concurrent misses
// for the same key share one os.OpenFile call, while different keys may still
// be opened concurrently.
//
// fresh is true only for the caller that created the canonical fd.
func (pool *FilePool) Open(path string, flag int, perm os.FileMode, ttl time.Duration) (file *File, fresh bool, err error) {
	key := cacheKey{path: path, flag: flag, perm: perm, ttl: ttl}

	for {
		pool.mu.Lock()
		toClose := pool.expireIdleLocked(pool.now())
		if f, ok := pool.files[key]; ok {
			if f.initializing {
				ready := f.ready
				pool.mu.Unlock()
				closeFiles(toClose)
				<-ready
				continue
			}
			pool.activateLocked(f)
			pool.mu.Unlock()
			closeFiles(toClose)
			return f, false, nil
		}

		if call, ok := pool.opening[key]; ok {
			pool.mu.Unlock()
			closeFiles(toClose)
			<-call.done
			if call.err != nil {
				return nil, false, call.err
			}
			continue
		}

		call := &openCall{done: make(chan struct{})}
		pool.opening[key] = call
		pool.mu.Unlock()
		closeFiles(toClose)

		fd, openErr := pool.openFile(path, flag, perm)

		pool.mu.Lock()
		delete(pool.opening, key)
		call.err = openErr
		if openErr == nil {
			file = &File{
				File:         fd,
				pool:         pool,
				key:          key,
				ready:        make(chan struct{}),
				refs:         1,
				initializing: true,
			}
			pool.files[key] = file
		}
		close(call.done)
		pool.mu.Unlock()

		if openErr != nil {
			return nil, false, openErr
		}
		return file, true, nil
	}
}

// File wraps an *os.File with a reference count managed by FilePool.
type File struct {
	expiresAt    time.Time
	File         *os.File
	pool         *FilePool
	idleElem     *list.Element
	ready        chan struct{}
	key          cacheKey
	refs         int
	initializing bool
	invalid      bool
	closed       bool
}

// Release releases one reference. The final reference moves a valid file into
// the idle LRU, or closes a file that has previously been invalidated.
func (f *File) Release() {
	if f == nil || f.pool == nil {
		return
	}

	pool := f.pool
	pool.mu.Lock()
	toClose := pool.expireIdleLocked(pool.now())
	if f.closed || f.refs == 0 {
		pool.mu.Unlock()
		closeFiles(toClose)
		return
	}

	pool.finishInitializationLocked(f)
	f.refs--
	if f.refs == 0 {
		if f.invalid {
			pool.markClosedLocked(f)
			toClose = append(toClose, f)
		} else {
			pool.makeIdleLocked(f, pool.now())
			toClose = append(toClose, pool.evictOverflowLocked()...)
		}
	}
	pool.mu.Unlock()
	closeFiles(toClose)
}

// Close invalidates the canonical fd and releases the caller's reference. New
// Open calls create a replacement fd. Existing users may finish before the old
// fd is closed by the final Release.
func (f *File) Close() {
	if f == nil || f.pool == nil {
		return
	}

	pool := f.pool
	pool.mu.Lock()
	toClose := pool.expireIdleLocked(pool.now())
	if f.closed {
		pool.mu.Unlock()
		closeFiles(toClose)
		return
	}

	if !f.invalid {
		pool.finishInitializationLocked(f)
		f.invalid = true
		if pool.files[f.key] == f {
			delete(pool.files, f.key)
		}
		pool.removeIdleLocked(f)
	}
	if f.refs > 0 {
		f.refs--
	}
	if f.refs == 0 {
		pool.markClosedLocked(f)
		toClose = append(toClose, f)
	}
	pool.mu.Unlock()
	closeFiles(toClose)
}

func (pool *FilePool) activateLocked(f *File) {
	pool.removeIdleLocked(f)
	f.refs++
}

func (pool *FilePool) finishInitializationLocked(f *File) {
	if f.initializing {
		f.initializing = false
		close(f.ready)
	}
}

func (pool *FilePool) makeIdleLocked(f *File, now time.Time) {
	f.idleElem = pool.idle.PushFront(f)
	if f.key.ttl > 0 {
		f.expiresAt = now.Add(f.key.ttl)
		if pool.nextExpiry.IsZero() || f.expiresAt.Before(pool.nextExpiry) {
			pool.nextExpiry = f.expiresAt
		}
	}
}

func (pool *FilePool) removeIdleLocked(f *File) {
	if f.idleElem != nil {
		pool.idle.Remove(f.idleElem)
		f.idleElem = nil
	}
	f.expiresAt = time.Time{}
}

func (pool *FilePool) markClosedLocked(f *File) {
	pool.finishInitializationLocked(f)
	pool.removeIdleLocked(f)
	if pool.files[f.key] == f {
		delete(pool.files, f.key)
	}
	f.invalid = true
	f.closed = true
	f.refs = 0
}

func (pool *FilePool) expireIdleLocked(now time.Time) (toClose []*File) {
	if pool.nextExpiry.IsZero() || now.Before(pool.nextExpiry) {
		return nil
	}

	pool.nextExpiry = time.Time{}
	for elem := pool.idle.Front(); elem != nil; {
		next := elem.Next()
		f := elem.Value.(*File)
		switch {
		case f.expiresAt.IsZero():
		case !now.Before(f.expiresAt):
			pool.markClosedLocked(f)
			toClose = append(toClose, f)
		case pool.nextExpiry.IsZero() || f.expiresAt.Before(pool.nextExpiry):
			pool.nextExpiry = f.expiresAt
		}
		elem = next
	}
	return toClose
}

func (pool *FilePool) evictOverflowLocked() (toClose []*File) {
	for pool.idle.Len() > maxIdleFiles {
		f := pool.idle.Back().Value.(*File)
		pool.markClosedLocked(f)
		toClose = append(toClose, f)
	}
	return toClose
}

func closeFiles(files []*File) {
	for _, f := range files {
		log.Debug().Str("path", f.key.path).Msg("close file")
		if err := f.File.Close(); err != nil {
			log.Warn().Err(err).Str("path", f.key.path).Msg("failed to close file")
		}
	}
}
