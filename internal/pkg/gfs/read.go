package gfs

import (
	"io"
	"slices"

	"github.com/docker/go-units"

	"tyr/internal/pkg/mempool"
)

func CopyReaderAt(dst io.Writer, ra io.ReaderAt, offset int64, size int64) (int64, error) {
	buf := mempool.Get()
	defer mempool.Put(buf)

	buf.B = slices.Grow(buf.B, units.MiB*4)

	if size >= units.MiB*4 {
		return io.CopyBuffer(dst, io.NewSectionReader(ra, offset, size), buf.B[:units.MiB*4])
	}

	// read and write it in one shot.

	n, err := ra.ReadAt(buf.B[:size], offset)

	if err != nil {
		return int64(n), err
	}

	n, err = dst.Write(buf.B[:size])

	return int64(n), err
}