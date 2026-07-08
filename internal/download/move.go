// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"neptune/internal/pkg/gfs"
)

func (d *Download) Move(target string) error {
	ctx, cancel := context.WithCancel(d.ctx)
	defer cancel()

	originalState := State(d.state.Load())

	if err := d.transition(Moving); err != nil {
		return err
	}

	d.s.mu.Lock()
	originalBasePath := d.s.basePath

	var selectedFilesSet map[int]struct{}
	if d.s.selectedFilesSet != nil {
		selectedFilesSet = make(map[int]struct{}, len(d.s.selectedFilesSet))
		for k := range d.s.selectedFilesSet {
			selectedFilesSet[k] = struct{}{}
		}
	}
	d.s.mu.Unlock()

	err := d.move(ctx, target, originalBasePath, selectedFilesSet)
	if err != nil {
		d.setError(err)
		return nil
	}

	d.s.mu.Lock()
	d.s.basePath = target
	d.s.mu.Unlock()

	if err := d.transition(originalState); err != nil {
		d.log.Error().Err(err).Msg("failed to restore state after move")
	}

	return nil
}

func (d *Download) move(ctx context.Context, target string, originalBasePath string, selectedFilesSet map[int]struct{}) error {
	for index := range d.info.Files {
		if selectedFilesSet != nil {
			if _, ok := selectedFilesSet[index]; !ok {
				continue
			}
		}
		err := d.moveFile(ctx, target, originalBasePath, uint32(index))
		if err != nil {
			return err
		}
	}

	for _, file := range d.info.Files {
		if err := os.Remove(filepath.Join(originalBasePath, file.Path)); err != nil {
			d.log.Warn().Err(err).Str("path", file.Path).Msg("failed to remove old file after move")
		}
	}

	if err := pruneEmptyDir(originalBasePath); err != nil {
		d.log.Warn().Err(err).Str("path", originalBasePath).Msg("failed to prune empty dir after move")
	}

	return nil
}

func (d *Download) moveFile(ctx context.Context, target string, originalBasePath string, index uint32) error {
	file := d.info.Files[index]

	targetPath := filepath.Join(target, file.Path)
	sourcePath := filepath.Join(originalBasePath, file.Path)

	err := os.MkdirAll(filepath.Dir(targetPath), os.ModePerm)
	if err != nil {
		return err
	}

	d.pieceDownloadRate.Reset()
	defer d.pieceDownloadRate.Reset()

	return gfs.SmartCopy(ctx, sourcePath, targetPath, d.pieceDownloadRate)
}

// PruneEmptyDirectories removes empty parent directories after the given path.
func PruneEmptyDirectories(osDirname string) error {
	return pruneEmptyDir(osDirname)
}

func pruneEmptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			sub := filepath.Join(dir, e.Name())
			subErr := pruneEmptyDir(sub)
			if subErr != nil {
				if errors.Is(subErr, fs.ErrNotExist) {
					continue
				}
				return subErr
			}
		}
	}

	entries, err = os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	if len(entries) > 0 {
		return nil
	}

	return os.Remove(dir)
}
