package gfs_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/go-units"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/gfs"
)

func TestCopy(t *testing.T) {
	dir := t.TempDir()

	src := filepath.Join(dir, "src.bin")
	out := filepath.Join(dir, "out.bin")

	srcFile := lo.Must(os.Create(src))
	defer srcFile.Close()

	lo.Must(io.CopyN(srcFile, rand.Reader, units.MiB*200))

	require.NoError(t, gfs.SmartCopy(context.Background(), src, out, nil))

	lo.Must(srcFile.Seek(0, io.SeekStart))

	outFile := lo.Must(os.Open(out))
	defer outFile.Close()

	same, err := sameReader(outFile, srcFile)

	require.NoError(t, err)
	require.True(t, same)
}

func sameReader(r1, r2 io.Reader) (identical bool, err error) {
	buf1 := bufio.NewReader(r1)
	buf2 := bufio.NewReader(r2)
	for {
		const sz = 1024
		scratch1 := make([]byte, sz)
		scratch2 := make([]byte, sz)
		n1, err1 := buf1.Read(scratch1)
		n2, err2 := buf2.Read(scratch2)
		if err1 != nil && err1 != io.EOF {
			return false, err1
		}
		if err2 != nil && err2 != io.EOF {
			return false, err2
		}
		if err1 == io.EOF || err2 == io.EOF {
			// we have to use direct compare here
			//goland:noinspection GoDirectComparisonOfErrors
			return err1 == err2, nil
		}
		if !bytes.Equal(scratch1[0:n1], scratch2[0:n2]) {
			return false, nil
		}
	}
}
