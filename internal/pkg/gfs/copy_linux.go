// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package gfs

import (
	"context"
	"io"
	"os"
	"syscall"

	"github.com/docker/go-units"
	"golang.org/x/sys/unix"

	"tyr/internal/pkg/flowrate"
)

func init() {
	// man page says it's available after kernel 4.5, but go stdlib only use it after kernel 53
	// https://github.com/golang/go/issues/36817#issuecomment-579151790
	major, minor := kernelVersion()
	if major > 5 || (major == 5 && minor >= 3) {
		supportCopyFileRange = true
	}
}

var supportCopyFileRange bool

func copyImpl(ctx context.Context, dest io.Writer, src io.Reader, buf []byte, monitor *flowrate.Monitor) error {
	if !supportCopyFileRange {
		return genericCopy(ctx, dest, src, buf)
	}

	in, okIn := src.(*os.File)
	out, okOut := dest.(*os.File)

	if okOut && okIn {
		return fileCopy(ctx, out, in, monitor)
	}

	return genericCopy(ctx, dest, src, buf)
}

func fileCopy(ctx context.Context, dest *os.File, src *os.File, monitor *flowrate.Monitor) error {
	s, err := src.Stat()
	if err != nil {
		return err
	}

	totalSize := s.Size()

	const size = units.MiB * 64
	var srcOffset int64 = 0
	var destOffset int64 = 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_, err := monitor.IO(unix.CopyFileRange(int(src.Fd()), &srcOffset, int(dest.Fd()), &destOffset, size, 0))
		if err != nil {
			return err
		}

		if srcOffset >= totalSize {
			return nil
		}
	}
}

// from https://go.dev/src/internal/syscall/unix/kernel_version_linux.go
func kernelVersion() (major, minor int) {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		return
	}

	var (
		values    [2]int
		value, vi int
	)
	for _, c := range uname.Release {
		if '0' <= c && c <= '9' {
			value = (value * 10) + int(c-'0')
		} else {
			// Note that we're assuming N.N.N here.
			// If we see anything else, we are likely to mis-parse it.
			values[vi] = value
			vi++
			if vi >= len(values) {
				break
			}
			value = 0
		}
	}

	return values[0], values[1]
}
