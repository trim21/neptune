// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) SetDownloadLimit(h metainfo.Hash, limit int64) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.SetDownloadLimit(limit)
	return nil
}

func (c *Client) SetUploadLimit(h metainfo.Hash, limit int64) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.SetUploadLimit(limit)
	return nil
}

func (c *Client) SetGlobalDownloadLimit(limit int64) {
	c.session.DownloadLimiter.Update(limit)
}

func (c *Client) SetGlobalUploadLimit(limit int64) {
	c.session.UploadLimiter.Update(limit)
}

func (c *Client) GetGlobalDownloadLimit() int64 {
	return c.session.DownloadLimiter.Rate()
}

func (c *Client) GetGlobalUploadLimit() int64 {
	return c.session.UploadLimiter.Rate()
}
