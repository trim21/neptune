package client

import (
	"fmt"

	"neptune/internal/metainfo"
)

func (c *Client) AddTags(h metainfo.Hash, tags []string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.AddTags(tags)
	return nil
}

func (c *Client) RemoveTags(h metainfo.Hash, tags []string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	for _, tag := range tags {
		d.RemoveTag(tag)
	}
	return nil
}

func (c *Client) SetCustom(h metainfo.Hash, key, value string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.SetCustom(key, value)
	return nil
}

func (c *Client) UpdateCustom(h metainfo.Hash, custom map[string]string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.UpdateCustom(custom)
	return nil
}

func (c *Client) DelCustom(h metainfo.Hash, key string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}
	d.DelCustom(key)
	return nil
}
