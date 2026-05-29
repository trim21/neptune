// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

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

	d.downloadLimiter.Update(limit)
	d.saveResume()

	return nil
}

func (c *Client) SetUploadLimit(h metainfo.Hash, limit int64) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.uploadLimiter.Update(limit)
	d.saveResume()

	return nil
}

func (c *Client) SetGlobalDownloadLimit(limit int64) {
	c.downloadLimiter.Update(limit)
}

func (c *Client) SetGlobalUploadLimit(limit int64) {
	c.uploadLimiter.Update(limit)
}
