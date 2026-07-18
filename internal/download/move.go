// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"neptune/internal/piece_store"
)

func (d *Download) RequestMove(target string) error {
	transition, err := d.transition(Moving)
	if err != nil {
		return err
	}
	originalState := transition.from
	d.stateCond.Broadcast()

	target, err = filepath.Abs(target)
	if err != nil {
		d.finishMove(originalState)
		return err
	}

	ctx, cancel := context.WithCancel(d.ctx)
	d.moveCancelMu.Lock()
	d.moveCancel = cancel
	if d.pieceDownloadRate != nil {
		d.pieceDownloadRate.Reset()
	}
	d.moveCancelMu.Unlock()
	if d.GetState() != Moving {
		cancel()
	}
	finished := false
	defer func() {
		cancel()
		if d.pieceDownloadRate != nil {
			d.pieceDownloadRate.Reset()
		}
		d.moveCancelMu.Lock()
		d.moveCancel = nil
		d.moveCancelMu.Unlock()
		if !finished {
			d.finishMove(originalState)
		}
	}()

	var copiedBytes int64
	report := func(progress piece_store.MoveProgress) {
		delta := progress.BytesCopied - copiedBytes
		copiedBytes = progress.BytesCopied
		if delta > 0 && d.pieceDownloadRate != nil {
			d.pieceDownloadRate.Update(int(delta))
		}
	}
	if err := d.store.Move(ctx, target, report); err != nil {
		return err
	}

	d.s.mu.Lock()
	d.s.basePath = target
	d.s.downloadDir = target
	d.s.mu.Unlock()
	d.finishMove(originalState)
	finished = true
	d.saveResume()

	return nil
}

func (d *Download) CancelMove() {
	d.moveCancelMu.RLock()
	cancel := d.moveCancel
	d.moveCancelMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (d *Download) finishMove(originalState State) {
	d.transitionMu.Lock()
	if State(d.state.Load()) == Moving {
		d.commitStateTransition(Moving, originalState)
	}
	d.transitionMu.Unlock()
	d.stateCond.Broadcast()
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
