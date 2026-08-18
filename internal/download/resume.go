// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"slices"
	"time"

	"neptune/internal/pkg/timestamp"
	"neptune/internal/session/store"
)

func (d *Download) saveResume() {
	if d.session.Store == nil {
		return
	}
	if err := d.session.Store.Upsert(d.resumeRecord()); err != nil {
		d.log.Err(err).Msg("failed to save download")
	}
}

func (d *Download) resumeRecord() *store.Resume {
	d.s.mu.RLock()
	defer d.s.mu.RUnlock()

	var selectedFiles []int
	if d.selectedFilesSet.Count() != uint32(len(d.info.Files)) {
		selectedFiles = make([]int, 0, d.selectedFilesSet.Count())
		d.selectedFilesSet.Range(func(i uint32) {
			selectedFiles = append(selectedFiles, int(i))
		})
		slices.Sort(selectedFiles)
	}

	return &store.Resume{
		BasePath:           d.s.basePath,
		Downloaded:         d.downloaded.Load(),
		Uploaded:           d.uploaded.Load(),
		Corrupted:          d.corrupted.Load(),
		Tags:               d.s.tags,
		Custom:             d.s.custom,
		State:              normalizeResumeState(d.GetState()),
		InfoHash:           d.info.Hash.Hex(),
		Bitfield:           d.completedBm.Bitfield(),
		AddAt:              timestamp.New(d.AddAt),
		CompletedAt:        timestamp.New(time.Unix(0, d.completedAt.Load())),
		SelectedFiles:      selectedFiles,
		FilePaths:          d.filePaths(),
		DownloadSpeedLimit: d.downloadLimiter.Rate(),
		UploadSpeedLimit:   d.uploadLimiter.Rate(),
		Trackers:           d.tracker.URLs(),
		TrackerKey:         d.tracker.Key,
		PiecePickStrategy:  uint32(d.GetPiecePickStrategy()),
		QueueWeight:        int64(d.QueueWeight()),
	}
}

func normalizeResumeState(s State) store.ResumeState {
	if s == Stopped {
		return store.ResumeStopped
	}
	return store.ResumeActive
}

func (d *Download) filePaths() []string {
	paths := make([]string, len(d.info.Files))
	for i, f := range d.info.Files {
		paths[i] = f.Path
	}
	return paths
}
