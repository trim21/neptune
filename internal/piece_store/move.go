// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog/log"

	"neptune/internal/pkg/gfs"
)

const moveCopyBufferSize = 1 << 20

const (
	invalidCrossDeviceLink = "invalid cross-device link"
	crossDeviceLink        = "cross-device link"
)

type moveFile struct {
	source   string
	target   string
	temp     string
	size     int64
	mode     os.FileMode
	promoted bool
}

// Move relocates all existing torrent data. An error means the old base path
// remains authoritative; cancellation is ignored after the commit phase starts.
func (s *FileStore) Move(ctx context.Context, target string, report MoveProgressFunc) error {
	progress := MoveProgress{Phase: MoveWaiting}
	reportMove(report, progress)

	s.opMu.Lock()
	defer s.opMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	sourceAbs, err := filepath.Abs(s.basePath)
	if err != nil {
		return fmt.Errorf("resolve source base path: %w", err)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("resolve target base path: %w", err)
	}
	if sourceAbs == targetAbs {
		progress.Phase = MoveCleaning
		reportMove(report, progress)
		return nil
	}
	if pathContains(sourceAbs, targetAbs) || pathContains(targetAbs, sourceAbs) {
		return fmt.Errorf("source and target base paths overlap: %q and %q", sourceAbs, targetAbs)
	}

	files, err := s.planMove(sourceAbs, targetAbs)
	if err != nil {
		return err
	}
	for _, file := range files {
		progress.BytesTotal += file.size
	}
	progress.FilesTotal = len(files)
	progress.Phase = MoveCopying
	reportMove(report, progress)

	if err = os.MkdirAll(targetAbs, os.ModePerm); err != nil {
		return fmt.Errorf("create target base path: %w", err)
	}
	targetIO := s.ioc.ForPath(targetAbs)
	buffer := make([]byte, moveCopyBufferSize)
	committed := false
	defer func() {
		if !committed {
			cleanupMoveTargets(files)
		}
	}()

	for i := range files {
		if err = ctx.Err(); err != nil {
			return err
		}
		file := &files[i]
		if err = os.MkdirAll(filepath.Dir(file.target), os.ModePerm); err != nil {
			return fmt.Errorf("create target directory for %q: %w", file.target, err)
		}
		file.temp, err = reserveMoveTemp(file.target)
		if err != nil {
			return err
		}

		if err = os.Link(file.source, file.temp); err == nil {
			progress.BytesDone += file.size
		} else if isCrossDeviceLinkError(err) {
			if err = copyMoveFile(ctx, s.diskIO, targetIO, file, buffer, &progress, report); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("hard link %q to %q: %w", file.source, file.temp, err)
		}

		progress.FilesDone++
		reportMove(report, progress)
	}

	progress.Phase = MoveCommitting
	reportMove(report, progress)

	paths := make([]string, 0, len(files)*2)
	for _, file := range files {
		paths = append(paths, file.source, file.target)
	}
	s.fp.InvalidatePaths(paths)

	for i := range files {
		file := &files[i]
		if _, err := os.Lstat(file.target); err == nil {
			return fmt.Errorf("move target already exists: %q", file.target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect move target %q: %w", file.target, err)
		}
		if err := os.Rename(file.temp, file.target); err != nil {
			return fmt.Errorf("commit move target %q: %w", file.target, err)
		}
		file.promoted = true
		file.temp = ""
	}

	s.basePath = targetAbs
	s.diskIO = targetIO
	committed = true

	progress.Phase = MoveCleaning
	reportMove(report, progress)
	for _, file := range files {
		if err := os.Remove(file.source); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn().Err(err).Str("path", file.source).Msg("failed to remove old file after move")
		}
	}
	pruneMovedDirectories(sourceAbs, files)
	return nil
}

func (s *FileStore) planMove(sourceBase, targetBase string) ([]moveFile, error) {
	files := make([]moveFile, 0, len(s.info.Files))
	for _, torrentFile := range s.info.Files {
		source := filepath.Join(sourceBase, torrentFile.Path)
		stat, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect move source %q: %w", source, err)
		}
		if !stat.Mode().IsRegular() {
			return nil, fmt.Errorf("move source is not a regular file: %q", source)
		}

		target := filepath.Join(targetBase, torrentFile.Path)
		if _, err := os.Lstat(target); err == nil {
			return nil, fmt.Errorf("move target already exists: %q", target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect move target %q: %w", target, err)
		}
		files = append(files, moveFile{
			source: source,
			target: target,
			size:   stat.Size(),
			mode:   stat.Mode().Perm(),
		})
	}
	return files, nil
}

func reserveMoveTemp(target string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".neptune-move-")
	if err != nil {
		return "", fmt.Errorf("reserve move target for %q: %w", target, err)
	}
	name := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return "", fmt.Errorf("close move target reservation %q: %w", name, closeErr)
	}
	if removeErr != nil {
		return "", fmt.Errorf("remove move target reservation %q: %w", name, removeErr)
	}
	return name, nil
}

func copyMoveFile(
	ctx context.Context,
	sourceIO *gfs.PathIO,
	targetIO *gfs.PathIO,
	file *moveFile,
	buffer []byte,
	progress *MoveProgress,
	report MoveProgressFunc,
) error {
	source, err := os.Open(file.source)
	if err != nil {
		return fmt.Errorf("open move source %q: %w", file.source, err)
	}
	defer source.Close()

	target, err := os.OpenFile(file.temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, file.mode)
	if err != nil {
		return fmt.Errorf("open move target %q: %w", file.temp, err)
	}

	var offset int64
	for offset < file.size {
		if err := ctx.Err(); err != nil {
			target.Close()
			return err
		}
		length := min(int64(len(buffer)), file.size-offset)
		n, readErr := sourceIO.ReadAtCtx(ctx, source, buffer[:length], offset)
		if n > 0 {
			written, writeErr := targetIO.WriteAtCtx(ctx, target, buffer[:n], offset)
			offset += int64(written)
			progress.BytesDone += int64(written)
			progress.BytesCopied += int64(written)
			reportMove(report, *progress)
			if writeErr != nil {
				target.Close()
				return fmt.Errorf("write move target %q: %w", file.temp, writeErr)
			}
			if written != n {
				target.Close()
				return fmt.Errorf("write move target %q: %w", file.temp, io.ErrShortWrite)
			}
		}
		if readErr != nil {
			target.Close()
			return fmt.Errorf("read move source %q: %w", file.source, readErr)
		}
		if n == 0 {
			target.Close()
			return fmt.Errorf("read move source %q: %w", file.source, io.ErrNoProgress)
		}
	}

	if err := target.Chmod(file.mode); err != nil {
		target.Close()
		return fmt.Errorf("set move target mode %q: %w", file.temp, err)
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("close move target %q: %w", file.temp, err)
	}
	return nil
}

func cleanupMoveTargets(files []moveFile) {
	for _, file := range files {
		if file.temp != "" {
			_ = os.Remove(file.temp)
		}
		if file.promoted {
			_ = os.Remove(file.target)
		}
	}
}

func pruneMovedDirectories(sourceBase string, files []moveFile) {
	for _, file := range files {
		for dir := filepath.Dir(file.source); pathContains(sourceBase, dir); dir = filepath.Dir(dir) {
			err := os.Remove(dir)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				break
			}
			if dir == sourceBase {
				break
			}
		}
	}
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func reportMove(report MoveProgressFunc, progress MoveProgress) {
	if report != nil {
		report(progress)
	}
}

func isCrossDeviceLinkError(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}
	return linkErr.Err.Error() == invalidCrossDeviceLink || linkErr.Err.Error() == crossDeviceLink
}
