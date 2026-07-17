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
	defer c.m.RUnlock()

	// Save resume, send tracker stopped events, and tear down each download
	// concurrently. Limit to 5 concurrent save operations to bound I/O.
	var wg conc.WaitGroup
	var sem = semaphore.NewWeighted(5)
	for _, d := range c.downloads {
		wg.Go(func() {
			_ = sem.Acquire(context.Background(), 1)
			d.SaveResume()
			sem.Release(1)

			d.Close()
		})
	}
	wg.Wait()

	c.session.Cancel()
	c.session.IOContext.Close()
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
