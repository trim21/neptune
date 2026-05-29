// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"os"
	"path/filepath"

	"github.com/karrick/godirwalk"

	"neptune/internal/pkg/gfs"
)

func (d *Download) Move(target string) error {
	ctx, cancel := context.WithCancel(d.ctx)
	defer cancel()

	d.m.RLock()
	originalState := d.state

	if originalState == Moving || originalState == Checking {
		d.m.RUnlock()
		return nil
	}

	d.state = Moving
	d.m.Unlock()

	err := d.move(ctx, target)
	if err != nil {
		d.setError(err)
		return nil
	}

	d.m.Lock()
	d.basePath = target
	d.state = originalState
	d.m.Unlock()

	return nil
}

func (d *Download) move(ctx context.Context, target string) error {
	originalBasePath := d.basePath

	for index := range d.info.Files {
		err := d.moveFile(ctx, target, uint32(index))
		if err != nil {
			return err
		}
	}

	for _, file := range d.info.Files {
		_ = os.Remove(filepath.Join(originalBasePath, file.Path))
	}

	_ = pruneEmptyDirectories(originalBasePath)

	return nil
}

func (d *Download) moveFile(ctx context.Context, target string, index uint32) error {
	file := d.info.Files[index]

	targetPath := filepath.Join(target, file.Path)
	sourcePath := filepath.Join(d.basePath, file.Path)

	err := os.MkdirAll(filepath.Dir(targetPath), os.ModePerm)
	if err != nil {
		return err
	}

	d.ioDown.Reset()
	defer d.ioDown.Reset()

	return gfs.SmartCopy(ctx, sourcePath, targetPath, d.ioDown)
}

func pruneEmptyDirectories(osDirname string) error {
	err := godirwalk.Walk(osDirname, &godirwalk.Options{
		Unsorted: true,
		Callback: func(_ string, _ *godirwalk.Dirent) error {
			// no-op while diving in; all the fun happens in PostChildrenCallback
			return nil
		},
		PostChildrenCallback: func(osPathname string, _ *godirwalk.Dirent) error {
			s, err := godirwalk.NewScanner(osPathname)
			if err != nil {
				return err
			}

			// Attempt to read only the first directory entry. Remember that
			// Scan skips both "." and ".." entries.
			hasAtLeastOneChild := s.Scan()

			// If error reading from directory, wrap up and return.
			if err := s.Err(); err != nil {
				return err
			}

			if hasAtLeastOneChild {
				return nil // do not remove directory with at least one child
			}

			return os.Remove(osPathname)
		},
	})

	return err
}
