// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"slices"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/gslice"
)

func (c *Client) AddTags(h metainfo.Hash, tags []string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.m.Lock()
	for _, tag := range tags {
		if !slices.Contains(d.tags, tag) {
			d.tags = append(d.tags, tag)
		}
	}
	d.m.Unlock()

	d.saveResume()

	return nil
}

func (c *Client) RemoveTags(h metainfo.Hash, tags []string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.m.Lock()
	for _, tag := range tags {
		d.tags = gslice.Remove(d.tags, tag)
	}
	d.m.Unlock()

	d.saveResume()

	return nil
}

func (c *Client) SetCustom(h metainfo.Hash, key, value string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.m.Lock()
	d.custom[key] = value
	d.m.Unlock()

	d.saveResume()

	return nil
}

func (c *Client) UpdateCustom(h metainfo.Hash, custom map[string]string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.m.Lock()
	for k, v := range custom {
		d.custom[k] = v
	}
	d.m.Unlock()

	d.saveResume()

	return nil
}

func (c *Client) DelCustom(h metainfo.Hash, key string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()

	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.m.Lock()
	delete(d.custom, key)
	d.m.Unlock()

	d.saveResume()

	return nil
}
