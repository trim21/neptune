// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/url"

	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/go-bencode"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/null"
)

type scrapeResponse struct {
	Files map[[20]byte]scrapeResponseFile `bencode:"files"`
}

type scrapeResponseFile struct {
	FailureReason null.String `bencode:"failure reason"`
	Complete      int         `bencode:"complete"`
	Downloaded    int         `bencode:"downloaded"`
	Incomplete    int         `bencode:"incomplete"`
}

func (c *Client) scrape() {
	var m = make(map[string][]metainfo.Hash, 20)

	c.m.RLock()
	defer c.m.RUnlock()

	for h, d := range c.downloadMap {
		if d.GetState()|Downloading|Seeding == 0 {
			continue
		}

		m[d.ScrapeURL()] = append(m[d.ScrapeURL()], h)
	}

	for scrapeUrl, hashes := range m {
		r := c.http.R()
		r.QueryParam = url.Values{"info_hash": lo.Map(hashes, func(item metainfo.Hash, index int) string {
			return item.AsString()
		})}

		res, err := r.Get(scrapeUrl)
		if err != nil {
			log.Info().Err(err).Msg("failed to scrape")
			continue
		}

		var resp scrapeResponse
		if err := bencode.Unmarshal(res.Body(), &resp); err != nil {
			return
		}

		_ = resp
	}
}
