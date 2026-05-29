// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) SetSpeedLimit(h metainfo.Hash, downloadLimit, uploadLimit int64) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.downloadLimiter.Update(downloadLimit)
	d.uploadLimiter.Update(uploadLimit)
	d.saveResume()

	return nil
}

func (c *Client) SetGlobalSpeedLimit(downloadLimit, uploadLimit int64) {
	c.downloadLimiter.Update(downloadLimit)
	c.uploadLimiter.Update(uploadLimit)
}
