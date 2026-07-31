// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"time"

	"neptune/internal/download"
)

// startGlobalTurnover runs the session-level peer turnover scheduler.
// Mirrors libtorrent's session_impl::tick peer_turnover logic: when the
// global connection count reaches the limit, evict low-value peers from
// the torrent with the most connections to free global slots.
func (c *Client) startGlobalTurnover() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-c.session.Ctx.Done():
			return
		case <-ticker.C:
			c.globalTurnover()
		}
	}
}

// globalTurnover evicts low-value peers from the torrent with the most
// connections when the global connection pool is exhausted. The freed
// global slots are then available to any torrent's new candidates.
func (c *Client) globalTurnover() {
	limit := int(c.session.Config.App.GlobalConnectionLimit)
	// Same guard as the per-torrent turnover: too low a limit makes
	// eviction disruptive.
	if limit <= 5 {
		return
	}

	// Only evict when the global connection pool approaches its limit
	// (TurnoverCutoff%): below the cutoff new connections are admitted
	// directly. Mirrors libtorrent's peer_turnover_cutoff.
	if int(c.session.ConnCount.Load()) < limit*download.TurnoverCutoff/100 {
		return
	}

	c.m.RLock()
	defer c.m.RUnlock()

	// Evict from the torrent with the most connections — it consumes the
	// most global slots, so freeing them helps the most.
	var busiest *download.Download
	for _, d := range c.downloads {
		if d.PeerCount() == 0 {
			continue
		}
		if busiest == nil || d.PeerCount() > busiest.PeerCount() {
			busiest = d
		}
	}
	if busiest == nil {
		return
	}

	const turnoverFraction = 100 / 4 // 4% of peers, mirrors libtorrent's peer_turnover
	n := max(busiest.PeerCount()/turnoverFraction, 1)
	busiest.EvictPeers(n)
}
