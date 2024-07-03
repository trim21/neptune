package gfs

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/docker/go-units"

	"tyr/internal/pkg/flowrate"
	"tyr/internal/pkg/mempool"
)

const invalidCrossDevice = "invalid cross-device link"
const crossDevice = "cross-device link"

// SmartCopy will try hardlink a file, and fallback to copy.
func SmartCopy(ctx context.Context, src string, dest string, monitor *flowrate.Monitor) error {
	// https://devblogs.microsoft.com/oldnewthing/20170707-00/?p=96555
	err := os.Link(src, dest)
	if err == nil {
		// job done
		return nil
	}

	var li *os.LinkError
	if !errors.As(err, &li) {
		return err
	}

	switch li.Err.Error() {
	case invalidCrossDevice, crossDevice:
	default:
		return err
	}

	// fallback to copy

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destFile.Close()

	buf := mempool.Get()
	defer mempool.Put(buf)

	buf.B = slices.Grow(buf.B, units.MiB)

	return Copy(ctx, destFile, srcFile, buf.B, monitor)
}
