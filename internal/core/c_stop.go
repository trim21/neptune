// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
	"golang.org/x/sync/semaphore"
)

func (c *Client) Shutdown() {
	log.Info().Msg("core shutting down...")

	c.m.RLock()
	defer c.m.RUnlock()

	c.saveSession()

	c.cancel()
}

func (c *Client) saveSession() *panics.Recovered {
	var w = conc.NewWaitGroup()

	var sem = semaphore.NewWeighted(5)

	for _, d := range c.downloads {
		// will only return ctx.Err() so we can ignore it here.
		_ = sem.Acquire(context.Background(), 1)

		w.Go(func() {
			defer sem.Release(1)

			b, err := d.MarshalBinary()
			if err != nil {
				log.Err(err).Msg("failed to save download")
				return
			}

			name := fmt.Sprintf("%x.resume", d.info.Hash)

			dirPath := filepath.Join(c.resumePath, name[:2])

			err = os.MkdirAll(dirPath, os.ModePerm)
			if err != nil {
				log.Err(err).Msg("failed to save download")
				return
			}

			err = os.WriteFile(filepath.Join(dirPath, name), b, os.ModePerm)
			if err != nil {
				log.Err(err).Msg("failed to save download")
			}
		})
	}

	return w.WaitAndRecover()
}
