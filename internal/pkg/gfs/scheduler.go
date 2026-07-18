// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gfs

import (
	"context"
	"os"

	"github.com/prometheus/client_golang/prometheus"

	"neptune/internal/pkg/disk_io"
)

type ioScheduler struct {
	manager *disk_io.Manager
}

func newIOScheduler(executor disk_io.Executor) ioScheduler {
	return ioScheduler{manager: disk_io.New(executor)}
}

func (s *ioScheduler) close() {
	s.manager.Close()
}

// PathIO routes pread and pwrite calls through the device queue selected for
// one torrent base path. A torrent is assumed not to span filesystems.
type PathIO struct {
	queue *disk_io.Queue
}

func (ioc *IOContext) ForPath(path string) *PathIO {
	return &PathIO{queue: ioc.scheduler.manager.QueueForPath(path)}
}

func (ioc *IOContext) Collectors() []prometheus.Collector {
	return ioc.scheduler.manager.Collectors()
}

func (p *PathIO) ReadAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	result := p.queue.Do(ctx, disk_io.PRead{
		File:   f,
		Buffer: buf,
		Offset: off,
	})
	return result.N, result.Err
}

func (p *PathIO) WriteAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	result := p.queue.Do(ctx, disk_io.PWrite{
		File:   f,
		Buffer: buf,
		Offset: off,
	})
	return result.N, result.Err
}
