// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"slices"
	"time"

	"github.com/rs/zerolog/log"

	"neptune/internal/download"
	"neptune/internal/pkg/empty"
)

// queueCandidate is a download ranked by its queue weight.
type queueCandidate struct {
	d      *Download
	weight int
}

// startQueueManager runs a periodic goroutine that rebalances downloads across
// the available download slots every 60 seconds. Downloads are ranked by
// QueueWeight (higher = higher priority). Downloads below the slow-speed
// threshold do not consume a slot.
func (c *Client) startQueueManager() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.session.Ctx.Done():
			return
		case <-c.queueRebalanceCh:
			c.rebalanceQueue()
		case <-ticker.C:
			c.rebalanceQueue()
		}
	}
}

// triggerQueueRebalance requests an immediate queue rebalance without waiting
// for the next 60s tick. Non-blocking (drop if a rebalance is already pending).
func (c *Client) triggerQueueRebalance() {
	select {
	case c.queueRebalanceCh <- empty.Empty{}:
	default:
	}
}

func (c *Client) rebalanceQueue() {
	downloadSlots := int(c.session.DownloadSlots.Load())
	if downloadSlots <= 0 {
		return
	}

	slowThreshold := c.session.SlowDownloadSpeedThreshold.Load()

	c.m.RLock()
	defer c.m.RUnlock()

	var candidates []queueCandidate
	for _, d := range c.downloads {
		if !d.IsDownloading() {
			continue
		}
		candidates = append(candidates, queueCandidate{d: d, weight: d.QueueWeight()})
	}

	if len(candidates) == 0 {
		return
	}

	// Sort descending by weight. Tie-break by AddedAt (older first).
	slices.SortStableFunc(candidates, func(a, b queueCandidate) int {
		if a.weight != b.weight {
			return b.weight - a.weight
		}
		return int(a.d.AddAt - b.d.AddAt)
	})

	used := 0
	for _, r := range candidates {
		isQueued := r.d.HasState(download.PendingDownloading)
		isSlow := slowThreshold > 0 && r.d.DownloadRate() < slowThreshold

		if !isQueued {
			if isSlow {
				continue
			}
			if used < downloadSlots {
				used++
				continue
			}
			log.Debug().Stringer("hash", r.d.InfoHash()).Msg("queue: demote (set queued flag)")
			r.d.DemoteToQueued()
			continue
		}

		if used < downloadSlots {
			used++
			log.Debug().Stringer("hash", r.d.InfoHash()).Msg("queue: promote (clear queued flag)")
			r.d.PromoteFromQueued()
		}
	}
}
