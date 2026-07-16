// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"fmt"
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
	defer d.s.mu.Unlock()

	if priority == 1 {
		d.resetReSelectedPieces(fileIDs)
	}

	for _, id := range fileIDs {
		if priority == 0 {
			d.selectedFilesSet.Unset(uint32(id))
		} else {
			d.selectedFilesSet.Set(uint32(id))
		}
	}

	d.rebuildWantedState()

	if d.completedBm.Count() == d.info.NumPieces {
		if err := d.transition(Seeding); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in SetFilePriority")
		}
	}

	d.saveResume()
	d.notifyPeersToRequest()

	return nil
}

// resetReSelectedPieces un-completes pieces that touch newly selected files
// so they are re-downloaded. Must be called under d.s.mu.
func (d *Download) resetReSelectedPieces(fileIDs []int) {
	fileIDSet := make(map[int]struct{}, len(fileIDs))
	for _, id := range fileIDs {
		fileIDSet[id] = struct{}{}
	}

	for pi := range d.info.NumPieces {
		if !d.completedBm.Contains(pi) {
			continue
		}
		if d.wantedBm.Contains(pi) {
			continue
		}

		touchesChanged := false
		for chunk := range d.info.PieceFileChunks(pi) {
			if _, ok := fileIDSet[chunk.FileIndex]; ok {
				touchesChanged = true
				break
			}
		}
		if !touchesChanged {
			continue
		}

		d.completedBm.Unset(pi)
		d.picker.Load().ResetPiece(pi)
	}
}

// CloseAllPeers closes all peer connections for this download.
func (d *Download) CloseAllPeers() {
	d.peers.Range(func(_ uint64, p Peer) bool {
		p.Close()
		return true
	})
}
