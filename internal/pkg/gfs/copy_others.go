// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build windows || darwin

package gfs

import (
	"context"
	"io"

	"neptune/internal/pkg/flowrate"
)

func copyImpl(ctx context.Context, dest io.Writer, src io.Reader, buf []byte, monitor *flowrate.Monitor) error {
	return genericCopy(ctx, dest, src, buf)
}
