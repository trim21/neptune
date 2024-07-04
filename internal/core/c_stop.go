// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rs/zerolog/log"
	"github.com/sourcegraph/conc"
	"github.com/sourcegraph/conc/panics"
)

func (c *Client) Shutdown() {
	log.Info().Msg("core shutting down...")

	c.m.Lock()
	defer c.m.Unlock()

	c.saveSession()

	c.cancel()
}

func (c *Client) saveSession() *panics.Recovered {
	var w = conc.NewWaitGroup()

	for _, d := range c.downloads {
		w.Go(func() {
			d.m.Lock()
			defer d.m.Unlock()

			b, err := d.MarshalBinary()
			if err != nil {
				log.Err(err).Msg("failed to save download")
				return
			}

			name := fmt.Sprintf("%x.resume", d.info.Hash)

			dirPath := filepath.Join(c.sessionPath, "resume", name[0:2])

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
