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

func (c *Client) NewDownload(
	m *metainfo.MetaInfo,
	info meta.Info,
	basePath string,
	tags []string,
	custom map[string]string,
	selectedFiles []int,
	skipHashCheck bool,
) (*Download, error) {
	return download.New(c.session, m, info, basePath, tags, custom, selectedFiles, download.InitState{
		State:             download.Checking,
		PiecePickStrategy: download.PiecePickStrategy(c.piecePickStrategy.Load()),
		SkipHashCheck:     skipHashCheck,
		TrackerStagger:    5 * time.Minute,
	})
}

func (c *Client) UnmarshalResume(data []byte, totalDownloads int) error {
	// Stagger the first announce of each resumed download by up to one second
	// per download (capped at 5 minutes) so a bulk resume of many torrents
	// does not announce all at once.
	d, err := download.LoadFromResume(c.session, data, time.Duration(min(totalDownloads, 300))*time.Second)
	if err != nil {
		return err
	}

	c.m.Lock()
	defer c.m.Unlock()
	c.downloads = append(c.downloads, d)
	c.downloadMap[d.InfoHash()] = d
	c.infoHashes = lo.Keys(c.downloadMap)
	keys := hashesToBytes(c.infoHashes)
	c.mseKeys.Store(&keys)

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
