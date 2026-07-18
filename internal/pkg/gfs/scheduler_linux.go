// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package gfs

import (
	"context"
	"os"

	"github.com/prometheus/client_golang/prometheus"

	"neptune/internal/pkg/diskio"
)

type ioScheduler struct {
	manager *diskio.Manager
}

func newIOScheduler() ioScheduler {
	return ioScheduler{manager: diskio.New()}
}

func (s *ioScheduler) close() {
	s.manager.Close()
}

// PathIO routes pread and pwrite calls through the device queue selected for
// one torrent base path. A torrent is assumed not to span filesystems.
type PathIO struct {
	ioc   *IOContext
	queue *diskio.Queue
}

func (ioc *IOContext) ForPath(path string) *PathIO {
	return &PathIO{ioc: ioc, queue: ioc.scheduler.manager.QueueForPath(path)}
}

func (ioc *IOContext) Collectors() []prometheus.Collector {
	return ioc.scheduler.manager.Collectors()
}

func (p *PathIO) ReadAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	var n int
	err := p.queue.Do(ctx, diskio.ClassRead, int64(len(buf)), func() error {
		var err error
		n, err = ReadAtCtx(ctx, p.ioc, f, buf, off)
		return err
	})
	return n, err
}

func (p *PathIO) WriteAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	var n int
	err := p.queue.Do(ctx, diskio.ClassWrite, int64(len(buf)), func() error {
		var err error
		n, err = WriteAtCtx(ctx, p.ioc, f, buf, off)
		return err
	})
	return n, err
}
