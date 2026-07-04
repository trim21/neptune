// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) StopTorrent(h metainfo.Hash) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	if d.HasState(Stopped) {
		return nil
	}

	return d.Stop()
}
