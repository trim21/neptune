package gfs

import (
	"context"
	"io"

	"tyr/internal/pkg/flowrate"
)

// Copy a file with ctx manager controlled
func Copy(ctx context.Context, dest io.Writer, src io.Reader, buf []byte, monitor *flowrate.Monitor) error {
	return copyImpl(ctx, dest, src, buf, monitor)
}

func genericCopy(ctx context.Context, dest io.Writer, src io.Reader, buf []byte) error {
	_, err := io.CopyBuffer(dest, NewReader(ctx, src), buf)

	return err
}
