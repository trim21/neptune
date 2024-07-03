package gfs

import (
	"context"
	"io"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func NewReader(ctx context.Context, r io.Reader) io.Reader {
	return &contextReader{ctx, r}
}

func (r *contextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}
