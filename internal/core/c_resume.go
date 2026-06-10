// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) ResumeTorrent(h metainfo.Hash) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	if !d.HasState(Stopped) {
		return fmt.Errorf("torrent %s is not stopped", h)
	}

	return d.Start()
}
