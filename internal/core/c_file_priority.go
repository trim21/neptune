// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/empty"
)

// SetFilePriority sets the download priority for the given files.
// priority 0 means skip (don't download), priority 1 means download.
func (c *Client) SetFilePriority(h metainfo.Hash, fileIDs []int, priority int) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

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

	d.m.Lock()

	// Initialize selectedFilesSet if nil (means all files were selected).
	if d.selectedFilesSet == nil {
		d.selectedFilesSet = make(map[int]struct{}, len(d.info.Files))
		for i := range d.info.Files {
			d.selectedFilesSet[i] = struct{}{}
		}
	}

	// When un-skipping files (priority=1), pieces that were force-marked done
	// by markUnselectedPiecesDone need to be cleared so they get re-downloaded.
	// A piece was force-done if it's in bm but doesn't touch any selected file.
	if priority == 1 {
		fileIDSet := make(map[int]struct{}, len(fileIDs))
		for _, id := range fileIDs {
			fileIDSet[id] = struct{}{}
		}

		for pi := range d.info.NumPieces {
			if !d.bm.Contains(pi) {
				continue
			}

			touchesChanged := false
			for _, fc := range d.pieceInfo[pi].fileChunks {
				if _, ok := fileIDSet[fc.fileIndex]; ok {
					touchesChanged = true
					break
				}
			}
			if !touchesChanged {
				continue
			}

			// Piece touches a changed file and is in bm.
			// If it doesn't touch any currently-selected file, it was force-done.
			if d.selectedPiecesBm.Contains(pi) {
				continue
			}

			d.bm.Unset(pi)
		}
	}

	// Update selectedFilesSet.
	for _, id := range fileIDs {
		if priority == 0 {
			delete(d.selectedFilesSet, id)
		} else {
			d.selectedFilesSet[id] = struct{}{}
		}
	}
	d.selectedSize.Store(d.computeSelectedSize())
	d.buildSelectedPiecesBm()

	// Mark pieces that only touch unselected files as done.
	d.markUnselectedPiecesDone()

	d.completed.Store(d.computeCompleted())

	// Transition to Seeding if all pieces are complete.
	if d.bm.Count() == d.info.NumPieces {
		d.state = Seeding
	}

	d.m.Unlock()

	d.saveResume()

	// Signal scheduler to re-evaluate piece selection.
	select {
	case d.scheduleRequestSignal <- empty.Empty{}:
	default:
	}

	return nil
}
