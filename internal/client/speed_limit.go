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

func (c *Client) SetQueueWeight(h metainfo.Hash, weight int) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.SetQueueWeight(weight)

	// If download slots are configured, rebalance so the new weight takes effect.
	if c.session.DownloadSlots.Load() > 0 {
		c.triggerQueueRebalance()
	}
	return nil
}

func (c *Client) SetDownloadSlots(slots uint16) {
	c.session.DownloadSlots.Store(uint32(slots))
	c.triggerQueueRebalance()
}

func (c *Client) GetDownloadSlots() uint16 {
	return uint16(c.session.DownloadSlots.Load())
}

func (c *Client) SetSlowDownloadSpeedThreshold(limit int64) {
	c.session.SlowDownloadSpeedThreshold.Store(limit)
}

func (c *Client) GetSlowDownloadSpeedThreshold() int64 {
	return c.session.SlowDownloadSpeedThreshold.Load()
}

func (c *Client) SetTorrentConnectionLimit(limit uint16) {
	c.session.TorrentConnLimit.Store(uint32(limit))
}

func (c *Client) GetTorrentConnectionLimit() uint16 {
	return uint16(c.session.TorrentConnLimit.Load())
}

func (c *Client) GetConnectionCount() uint32 {
	return c.session.ConnCount.Load()
}
