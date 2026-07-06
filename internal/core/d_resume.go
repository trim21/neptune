// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"encoding"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
)

var _ encoding.BinaryMarshaler = (*Download)(nil)

type resume struct {
	BasePath      string
	InfoHash      string
	Bitfield      []byte
	Tags          []string
	Custom        map[string]string
	Trackers      [][]string
	SelectedFiles []int // indices of files selected for download. nil means all files.
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

func (d *Download) resumeFilePath() (dir, file string) {
	name := fmt.Sprintf("%x.resume", d.info.Hash)

	dir = filepath.Join(d.c.resumePath, name[:2])

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
	d.m.RLock()
	defer d.m.RUnlock()
	var selectedFiles []int
	if d.selectedFilesSet != nil {
		selectedFiles = make([]int, 0, len(d.selectedFilesSet))
		for idx := range d.selectedFilesSet {
			selectedFiles = append(selectedFiles, idx)
		}
		slices.Sort(selectedFiles)
	}
	basePath := d.basePath

	return bencode.Marshal(resume{
		BasePath:           basePath,
		Downloaded:         d.downloaded.Load(),
		Uploaded:           d.uploaded.Load(),
		Corrupted:          d.corrupted.Load(),
		Tags:               d.tags,
		Custom:             d.custom,
		State:              d.GetState(),
		InfoHash:           d.info.Hash.Hex(),
		Bitfield:           d.completedBm.Bitfield(),
		AddAt:              d.AddAt,
		CompletedAt:        d.CompletedAt.Load(),
		SelectedFiles:      selectedFiles,
		DownloadSpeedLimit: d.downloadLimiter.Rate(),
		UploadSpeedLimit:   d.uploadLimiter.Rate(),
		Trackers:           d.Trk.URLs(),
	})
}

func (c *Client) UnmarshalResume(data []byte, totalDownloads int) error {
	var r resume
	if err := bencode.Unmarshal(data, &r); err != nil {
		return errgo.Wrap(err, "failed to decode resume data")
	}

	tPath := filepath.Join(c.torrentPath, r.InfoHash[:2], r.InfoHash[2:4], r.InfoHash+".torrent")
	var m, err = metainfo.LoadFromFile(tPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errgo.Wrap(err, fmt.Sprintf("torrent %s missing at %s", r.InfoHash, tPath))
		}

		return errgo.Wrap(err, "failed to decode torrent file "+tPath)
	}

	info, err := meta.FromTorrent(*m)
	if err != nil {
		return errgo.Wrap(err, "failed to decode torrent data")
	}

	// backward compatibility: old resume data without SelectedFiles defaults to all files
	if r.SelectedFiles == nil {
		r.SelectedFiles = make([]int, len(info.Files))
		for i := range info.Files {
			r.SelectedFiles[i] = i
		}
	}

	d := c.NewDownload(m, info, r.BasePath, r.Tags, r.Custom, r.SelectedFiles)

	// unsafe methods are safe here because d hasn't been shared with other goroutines yet.
	d.completedBm = bm.FromBitfields(r.Bitfield, d.info.NumPieces)
	for i := range d.info.NumPieces {
		if d.completedBm.Contains(i) {
			d.picker.weHave(i)
		}
	}
	d.markUnselectedPiecesDoneUnsafe()
	d.completed.Store(d.computeCompletedUnsafe())
	d.state.Store(uint32(r.State))
	d.AddAt = r.AddAt
	d.CompletedAt.Store(r.CompletedAt)

	d.downloaded.Store(r.Downloaded)
	d.downloadAtStart = r.Downloaded

	d.uploaded.Store(r.Uploaded)
	d.corrupted.Store(r.Corrupted)
	d.uploadAtStart = r.Uploaded

	d.downloadLimiter.Update(r.DownloadSpeedLimit)
	d.uploadLimiter.Update(r.UploadSpeedLimit)

	d.Trk.Stagger(totalDownloads)

	c.m.Lock()
	defer c.m.Unlock()

	c.downloads = append(c.downloads, d)
	c.downloadMap[info.Hash] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	d.Init(true, true)

	return nil
}
