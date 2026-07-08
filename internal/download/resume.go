// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"encoding"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/rs/zerolog/log"
	"github.com/trim21/go-bencode"
)

var _ encoding.BinaryMarshaler = (*Download)(nil)

type resume struct {
	BasePath      string
	InfoHash      string
	Bitfield      []byte
	Tags          []string
	Custom        map[string]string
	Trackers      [][]string
	SelectedFiles []int    // indices of files selected for download. nil means all files.
	FilePaths     []string // file paths (relative to BasePath), persisted to survive truncation algorithm changes
	// Per-torrent speed limits in bytes per second. 0 means unlimited.
	DownloadSpeedLimit int64
	UploadSpeedLimit   int64
	AddAt              int64
	CompletedAt        int64
	Downloaded         int64
	Uploaded           int64
	Corrupted          int64
	State              State
}

func (d *Download) filePaths() []string {
	paths := make([]string, len(d.info.Files))
	for i, f := range d.info.Files {
		paths[i] = f.Path
	}
	return paths
}

func (d *Download) resumeFilePath() (dir, file string) {
	name := fmt.Sprintf("%x.resume", d.info.Hash)

	dir = filepath.Join(d.session.ResumePath, name[:2])

	return dir, filepath.Join(dir, name)
}

func (d *Download) saveResume() {
	b, err := d.MarshalBinary()
	if err != nil {
		d.log.Err(err).Msg("failed to save download")
		return
	}

	dir, file := d.resumeFilePath()

	err = os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		log.Err(err).Msg("failed to save download")
		return
	}

	err = os.WriteFile(file, b, os.ModePerm)
	if err != nil {
		log.Err(err).Msg("failed to save download")
	}
}

func (d *Download) MarshalBinary() (data []byte, err error) {
	d.s.mu.RLock()
	defer d.s.mu.RUnlock()
	var selectedFiles []int
	if d.s.selectedFilesSet != nil {
		selectedFiles = make([]int, 0, len(d.s.selectedFilesSet))
		for idx := range d.s.selectedFilesSet {
			selectedFiles = append(selectedFiles, idx)
		}
		slices.Sort(selectedFiles)
	}
	basePath := d.s.basePath

	return bencode.Marshal(resume{
		BasePath:           basePath,
		Downloaded:         d.downloaded.Load(),
		Uploaded:           d.uploaded.Load(),
		Corrupted:          d.corrupted.Load(),
		Tags:               d.s.tags,
		Custom:             d.s.custom,
		State:              d.GetState(),
		InfoHash:           d.info.Hash.Hex(),
		Bitfield:           d.completedBm.Bitfield(),
		AddAt:              d.AddAt,
		CompletedAt:        d.CompletedAt.Load(),
		SelectedFiles:      selectedFiles,
		FilePaths:          d.filePaths(),
		DownloadSpeedLimit: d.downloadLimiter.Rate(),
		UploadSpeedLimit:   d.uploadLimiter.Rate(),
		Trackers:           d.Trk.URLs(),
	})
}
