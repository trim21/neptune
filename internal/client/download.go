// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
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
	// Only stagger incomplete downloads; seeds don't need delay
	// because they already have all pieces and just need to announce presence.
	if !d.HasState(download.Seeding) {
		d.TrkStagger(totalDownloads)
	}

	c.m.Lock()
	defer c.m.Unlock()
	c.downloads = append(c.downloads, d)
	c.downloadMap[d.InfoHash()] = d
	c.infoHashes = lo.Keys(c.downloadMap)

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
	return d.Move(targetBasePath)
}
