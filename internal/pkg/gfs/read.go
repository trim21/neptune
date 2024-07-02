//go:build gc

// zeropool assume compiler gc

package gfs

import (
	"io"

	"github.com/colega/zeropool"
	"github.com/docker/go-units"
)

// pool return a byte slice with cap = 4mib
// do not resize it in any case.
// use mempool.Get caller need a byte slice can be resized
var pool = zeropool.New(func() []byte {
	return make([]byte, units.MiB*4)
})

func CopyReaderAt(dst io.Writer, ra io.ReaderAt, offset int64, size int64) (int64, error) {
	buf := pool.Get()
	defer pool.Put(buf)

	if size >= units.MiB*4 {
		return io.CopyBuffer(dst, io.NewSectionReader(ra, offset, size), buf)
	}

	// read and write it in one shot.

	n, err := ra.ReadAt(buf[:size], offset)

	if err != nil {
		return int64(n), err
	}

	n, err = dst.Write(buf[:size])

	return int64(n), err
}
