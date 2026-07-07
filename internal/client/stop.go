// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"context"

	"github.com/rs/zerolog/log"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
	"golang.org/x/sync/semaphore"
)

func (c *Client) Shutdown() {
	log.Info().Msg("core shutting down...")

	c.m.RLock()
	downloads := c.downloads
	c.saveSessionUnsafe()
	c.m.RUnlock()

	// Send Eventdownload.Stopped to all trackers before cancelling contexts.
	// Each tracker request has a 5-second timeout.
	for _, d := range downloads {
		d.Trk.Shutdown()
	}

	c.session.Cancel()
}

func (c *Client) saveSessionUnsafe() *panics.Recovered {
	var w = conc.NewWaitGroup()

	var sem = semaphore.NewWeighted(5)

	for _, d := range c.downloads {
		// will only return ctx.Err() so we can ignore it here.
		_ = sem.Acquire(context.Background(), 1)

		w.Go(func() {
			defer sem.Release(1)
			d.SaveResume()
		})
	}

	return w.WaitAndRecover()
}
