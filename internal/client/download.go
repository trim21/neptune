// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"time"

	"github.com/samber/lo"

	"neptune/internal/download"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
)

func (c *Client) NewDownload(m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string, custom map[string]string, selectedFiles []int) *Download {
	d := download.New(c.session, m, info, basePath, tags, custom, selectedFiles)

	// Apply the client-level default piece pick strategy, which may differ
	// from the config file value if the user changed it via RPC.
	strategy := download.PiecePickStrategy(c.piecePickStrategy.Load())
	d.SetPiecePickStrategy(strategy)

	return d
}

func (c *Client) UnmarshalResume(data []byte, totalDownloads int) error {
	d, err := download.ResumeFromData(c.session, data)
	if err != nil {
		return err
	}
	// Stagger announces across the session to avoid all torrents
	// announcing simultaneously on restart. Downloading torrents
	// get a shorter window (60s) for timely reconnect; seeding/stopped
	// torrents spread across the full session count.
	maxDelay := time.Duration(totalDownloads) * time.Second
	if d.IsDownloading() {
		maxDelay = min(maxDelay, 60*time.Second)
	}
	d.TrkStagger(maxDelay)

	c.m.Lock()
	defer c.m.Unlock()
	c.downloads = append(c.downloads, d)
	c.downloadMap[d.InfoHash()] = d
	c.infoHashes = lo.Keys(c.downloadMap)
	keys := hashesToBytes(c.infoHashes)
	c.mseKeys.Store(&keys)

	d.Init(true, true)
	return nil
}

func (c *Client) ScheduleMove(ih metainfo.Hash, targetBasePath string) error {
	c.m.RLock()
	d, ok := c.downloadMap[ih]
	c.m.RUnlock()
	if !ok {
		return download.ErrTorrentNotFound
	}
	return d.RequestMove(targetBasePath)
}
