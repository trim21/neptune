//go:build windows || darwin

package gfs

import (
	"context"
	"io"

	"tyr/internal/pkg/flowrate"
)

func copyImpl(ctx context.Context, dest io.Writer, src io.Reader, buf []byte, monitor *flowrate.Monitor) error {
	return genericCopy(ctx, dest, src, buf)
}
