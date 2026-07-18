// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

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

func newIOScheduler(executor diskio.Executor) ioScheduler {
	return ioScheduler{manager: diskio.New(executor)}
}

func (s *ioScheduler) close() {
	s.manager.Close()
}

// PathIO routes pread and pwrite calls through the device queue selected for
// one torrent base path. A torrent is assumed not to span filesystems.
type PathIO struct {
	queue *diskio.Queue
}

func (ioc *IOContext) ForPath(path string) *PathIO {
	return &PathIO{queue: ioc.scheduler.manager.QueueForPath(path)}
}

func (ioc *IOContext) Collectors() []prometheus.Collector {
	return ioc.scheduler.manager.Collectors()
}

func (p *PathIO) ReadAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	result := p.queue.Do(ctx, diskio.PRead{
		File:   f,
		Buffer: buf,
		Offset: off,
	})
	return result.N, result.Err
}

func (p *PathIO) WriteAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	result := p.queue.Do(ctx, diskio.PWrite{
		File:   f,
		Buffer: buf,
		Offset: off,
	})
	return result.N, result.Err
}
