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

	if !d.HasState(download.Stopped) && !d.HasState(download.Downloading) && !d.HasState(download.Seeding) {
		return fmt.Errorf("torrent %s is not in a startable state, current state: %s, err: %q", h, d.GetState().String(), d.ErrorMsg())
	}

	return d.Start()
}
