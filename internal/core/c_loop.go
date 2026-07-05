// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"time"
)

func (c *Client) startGlobalLoops() {
	go c.globalUnchokeLoop()
	go c.globalOptimisticUnchokeLoop()
}

func (c *Client) globalUnchokeLoop() {
	ticker := time.NewTicker(unchokeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.m.RLock()
			for _, d := range c.downloads {
				if d.HasState(Downloading | Seeding) {
					d.recalculateUnchokeSlots()
					d.recalcPeerCounts()
				}
			}
			c.m.RUnlock()
		}
	}
}

func (c *Client) globalOptimisticUnchokeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.m.RLock()
			for _, d := range c.downloads {
				if d.HasState(Downloading | Seeding) {
					d.optimisticUnchoke()
				}
			}
			c.m.RUnlock()
		}
	}
}
