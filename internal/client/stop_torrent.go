// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"fmt"

	"neptune/internal/download"
	"neptune/internal/metainfo"
)

func (c *Client) StopTorrent(h metainfo.Hash) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	if d.HasState(download.Stopped) {
		return nil
	}

	if err := d.Stop(); err != nil {
		return err
	}

	// If download slots are configured, rebalance to promote a queued torrent
	// into the newly freed slot.
	if c.session.DownloadSlots.Load() > 0 {
		c.triggerQueueRebalance()
	}

	return nil
}
