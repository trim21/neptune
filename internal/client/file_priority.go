package client

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) SetFilePriority(h metainfo.Hash, fileIDs []int, priority int) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	return d.SetFilePriority(fileIDs, priority)
}
