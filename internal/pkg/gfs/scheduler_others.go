// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package gfs

import (
	"context"
	"os"

	"github.com/prometheus/client_golang/prometheus"
)

type ioScheduler struct{}

func newIOScheduler() ioScheduler { return ioScheduler{} }

func (*ioScheduler) close() {}

// PathIO directly uses the portable pread and pwrite implementation.
type PathIO struct {
	ioc *IOContext
}

func (ioc *IOContext) ForPath(_ string) *PathIO {
	return &PathIO{ioc: ioc}
}

func (*IOContext) Collectors() []prometheus.Collector { return nil }

func (p *PathIO) ReadAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	return ReadAtCtx(ctx, p.ioc, f, buf, off)
}

func (p *PathIO) WriteAtCtx(ctx context.Context, f *os.File, buf []byte, off int64) (int, error) {
	return WriteAtCtx(ctx, p.ioc, f, buf, off)
}
