// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"net/netip"
	"time"

	"github.com/samber/lo"
	"github.com/samber/lo/mutable"

	"neptune/internal/client/tracker"
	"neptune/internal/metainfo"
)

// Trackers returns tracker metadata for API responses.
func (d *Download) Trackers() []tracker.Info {
	return d.tracker.List()
}

// AddTracker adds a tracker URL at the given tier.
func (d *Download) AddTracker(url string, tier int) {
	d.tracker.Add(url, tier)
}

// RemoveTracker removes a tracker URL from all tiers.
func (d *Download) RemoveTracker(url string) {
	d.tracker.Remove(url)
}

// ReplaceTrackers replaces tracker URLs matching keys with their mapped values.
func (d *Download) ReplaceTrackers(replacements map[string]string) {
	d.tracker.Replace(replacements)
}

// Reannounce triggers an immediate reannounce to all trackers with the given event.
// Returns false if the earliest announce interval hasn't expired.
func (d *Download) Reannounce(event tracker.AnnounceEvent) bool {
	return d.tracker.ForceReannounce(event)
}

// TrackerURLs returns all tracker URLs.
func (d *Download) TrackerURLs() []string {
	var urls []string
	d.tracker.Each(func(_ int, tr *tracker.Tracker) {
		urls = append(urls, tr.URL)
	})
	return urls
}

// StoreTrackerStats stores seed/leecher counts from a scrape response.
func (d *Download) StoreTrackerStats(url string, seeds, leechers int) {
	d.tracker.Seeds.Store(url, seeds)
	d.tracker.Leechers.Store(url, leechers)
}

// Close tears down the download: sends tracker stopped events, cancels the
// context, waits for background goroutines, and closes all peer connections.
func (d *Download) Close() {
	d.Cancel()
	d.tracker.Shutdown()
	d.BackgroundWgWait()
	d.CloseAllPeers()
}

func (d *Download) setAnnounceList(list metainfo.AnnounceList) {
	tiers := make([]tracker.TrackerTier, 0, len(list))
	for _, tier := range list {
		mutable.Shuffle(tier)
		tiers = append(tiers, tracker.TrackerTier{
			Trackers: lo.Map(tier, func(item string, _ int) *tracker.Tracker {
				return &tracker.Tracker{URL: item, NextAnnounce: time.Now()}
			}),
		})
	}
	d.tracker.SetTiers(tiers)
}

func (d *Download) trackerTotals() (seeders, leechers int) {
	return d.tracker.Totals()
}

func (d *Download) trackerErrors() map[string]string {
	m := make(map[string]string)
	d.tracker.Errors.Range(func(url string, msg string) bool {
		m[url] = msg
		return true
	})
	return m
}

type peerWithPriority struct {
	addrPort netip.AddrPort
	priority uint32
}

func (p peerWithPriority) Less(o peerWithPriority) bool {
	return p.priority > o.priority
}
