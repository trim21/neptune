// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"fmt"

	"neptune/internal/download"
	"neptune/internal/metainfo"
)

func (c *Client) StartTorrent(h metainfo.Hash) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	// Already running (Downloading, Seeding, PendingDownloading) or Checking — no-op.
	if d.IsDownloading() || d.HasState(download.Seeding) || d.HasState(download.Checking) {
		return nil
	}

	if !d.HasState(download.Stopped) {
		return fmt.Errorf("torrent %s is not in a startable state, current state: %s, err: %q", h, d.GetState().String(), d.ErrorMsg())
	}

	if err := d.Start(); err != nil {
		return err
	}

	// If download slots are configured, rebalance immediately so the
	// queue manager can decide whether this torrent gets a slot.
	if c.session.DownloadSlots.Load() > 0 {
		c.triggerQueueRebalance()
	}

	return nil
}
