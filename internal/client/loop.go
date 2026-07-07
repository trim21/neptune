package client

import (
	"time"
)

func (c *Client) startGlobalLoops() {
	go c.globalUnchokeLoop()
	go c.globalOptimisticUnchokeLoop()
}

func (c *Client) globalUnchokeLoop() {
	ticker := time.NewTicker(UnchokeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.session.Ctx.Done():
			return
		case <-ticker.C:
			c.m.RLock()
			for _, d := range c.downloads {
				if d.HasState(Downloading | Seeding) {
					d.RecalculateUnchokeSlots()
					d.RecalcPeerCounts()
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
		case <-c.session.Ctx.Done():
			return
		case <-ticker.C:
			c.m.RLock()
			for _, d := range c.downloads {
				if d.HasState(Downloading | Seeding) {
					d.OptimisticUnchoke()
				}
			}
			c.m.RUnlock()
		}
	}
}
