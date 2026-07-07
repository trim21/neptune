// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"fmt"
	"os"
	"path/filepath"

	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/fallocate"
)

// SetFilePriority sets the download priority for the given files.
// priority 0 means skip (don't download), priority 1 means download.
func (d *Download) SetFilePriority(fileIDs []int, priority int) error {
	if priority != 0 && priority != 1 {
		return fmt.Errorf("invalid priority %d, must be 0 or 1", priority)
	}

	if len(fileIDs) == 0 {
		return nil
	}

	for _, id := range fileIDs {
		if id < 0 || id >= len(d.info.Files) {
			return fmt.Errorf("invalid file id %d, torrent has %d files", id, len(d.info.Files))
		}
	}

	d.s.mu.Lock()

	if d.s.selectedFilesSet == nil {
		d.s.selectedFilesSet = make(map[int]struct{}, len(d.info.Files))
		for i := range d.info.Files {
			d.s.selectedFilesSet[i] = struct{}{}
		}
	}

	if priority == 1 {
		fileIDSet := make(map[int]struct{}, len(fileIDs))
		for _, id := range fileIDs {
			fileIDSet[id] = struct{}{}
		}

		for pi := range d.info.NumPieces {
			if !d.completedBm.Contains(pi) {
				continue
			}

			touchesChanged := false
			for _, fc := range d.pieceInfo.FileChunks(pi) {
				if _, ok := fileIDSet[fc.FileIndex]; ok {
					touchesChanged = true
					break
				}
			}
			if !touchesChanged {
				continue
			}

			if d.wantedBm.Contains(pi) {
				continue
			}

			d.completedBm.Unset(pi)
			d.picker.resetPiece(pi, d.info)
		}
	}

	for _, id := range fileIDs {
		tf := d.info.Files[id]
		filePath := filepath.Join(d.s.basePath, tf.Path)

		if priority == 0 {
			delete(d.s.selectedFilesSet, id)
			if d.session.Config.App.Fallocate {
				if f, err := os.OpenFile(filePath, os.O_WRONLY, 0); err == nil {
					_ = f.Truncate(0)
					_ = f.Truncate(tf.Length)
					f.Close()
				}
			}
		} else {
			d.s.selectedFilesSet[id] = struct{}{}
			if d.session.Config.App.Fallocate {
				if f, err := os.OpenFile(filePath, os.O_WRONLY, 0); err == nil {
					_ = fallocate.Fallocate(f, 0, tf.Length)
					f.Close()
				}
			}
		}
	}

	d.selectedSize.Store(d.computeSelectedSizeUnsafe())
	d.buildSelectedPiecesBmUnsafe()
	d.markUnselectedPiecesDoneUnsafe()
	d.completed.Store(d.computeCompletedUnsafe())

	if d.completedBm.Count() == d.info.NumPieces {
		if err := d.transition(Seeding); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in SetFilePriority")
		}
	}

	d.s.mu.Unlock()

	d.saveResume()

	select {
	case d.scheduleRequestSignal <- empty.Empty{}:
	default:
	}

	return nil
}

// CloseAllPeers closes all peer connections for this download.
func (d *Download) CloseAllPeers() {
	d.peers.Range(func(_ uint64, p *Peer) bool {
		p.close()
		return true
	})
}
