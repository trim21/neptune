// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"encoding"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/global/tasks"
)

var _ encoding.BinaryMarshaler = (*Download)(nil)

type resume struct {
	BasePath    string
	Bitmap      []byte
	Tags        []string
	Trackers    [][]string
	AddAt       int64
	CompletedAt int64
	Downloaded  int64
	Uploaded    int64
	State       State
	InfoHash    string
}

func (d *Download) MarshalBinary() (data []byte, err error) {
	return bencode.Marshal(resume{
		BasePath:    d.basePath,
		Downloaded:  d.downloaded.Load(),
		Uploaded:    d.uploaded.Load(),
		Tags:        d.tags,
		State:       d.GetState(),
		InfoHash:    d.info.Hash.Hex(),
		AddAt:       d.AddAt,
		CompletedAt: d.CompletedAt.Load(),
		Trackers: lo.Map(d.trackers, func(tier TrackerTier, index int) []string {
			return lo.Map(tier.trackers, func(tracker *Tracker, index int) string {
				return tracker.url
			})
		}),
	})
}

func (c *Client) UnmarshalResume(data []byte) error {
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

		return errgo.Wrap(err, fmt.Sprintf("failed to decode torrent file %s", tPath))
	}

	info, err := meta.FromTorrent(*m)
	if err != nil {
		return errgo.Wrap(err, "failed to decode torrent data")
	}

	d := c.NewDownload(m, info, r.BasePath, r.Tags)

	d.state = r.State
	d.AddAt = r.AddAt
	d.CompletedAt.Store(d.CompletedAt.Load())

	d.downloaded.Store(r.Downloaded)
	d.downloadAtStart = r.Downloaded

	d.uploaded.Store(r.Uploaded)
	d.uploadAtStart = r.Uploaded

	c.m.Lock()
	defer c.m.Unlock()

	c.downloads = append(c.downloads, d)
	c.downloadMap[info.Hash] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	tasks.Submit(d.Init)

	return nil
}
