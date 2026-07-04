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

// trackerRef references a specific tracker within a download.
type trackerRef struct {
	download *Download
	tracker  *Tracker
}

func (c *Client) scrape() {
	// Map from scrape URL to list of (download, tracker) pairs.
	m := make(map[string][]trackerRef, 20)
	// Map from scrape URL to list of info_hashes for the HTTP request.
	hashes := make(map[string][]metainfo.Hash, 20)

	c.m.RLock()
	defer c.m.RUnlock()

	for _, d := range c.downloadMap {
		if !d.HasState(Downloading | Seeding) {
			continue
		}

		d.m.RLock()
		for _, tier := range d.trackers {
			for _, t := range tier.trackers {
				if scrapeURL, ok := announceToScrape(t.url); ok {
					m[scrapeURL] = append(m[scrapeURL], trackerRef{download: d, tracker: t})
					hashes[scrapeURL] = append(hashes[scrapeURL], d.info.Hash)
				}
			}
		}
		d.m.RUnlock()
	}

	for scrapeURL, refs := range m {
		r := c.http.R()
		r.QueryParam = url.Values{"info_hash": lo.Map(hashes[scrapeURL], func(item metainfo.Hash, _ int) string {
			return item.AsString()
		})}

		res, err := r.Get(scrapeURL)
		if err != nil {
			log.Info().Err(err).Msg("failed to scrape")
			continue
		}

		var resp scrapeResponse
		if err := bencode.Unmarshal(res.Body(), &resp); err != nil {
			log.Info().Err(err).Msg("failed to parse scrape response")
			continue
		}

		for _, ref := range refs {
			if file, ok := resp.Files[ref.download.info.Hash]; ok {
				ref.download.trackerSeeds.Store(ref.tracker.url, file.Complete)
				ref.download.trackerLeechers.Store(ref.tracker.url, file.Incomplete)
			}
		}
	}
}
