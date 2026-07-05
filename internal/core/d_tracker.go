// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/netip"
	"time"

	"github.com/samber/lo"
	"github.com/samber/lo/mutable"

	"neptune/internal/core/tracker"
	"neptune/internal/metainfo"
)

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
	d.Trk.SetTiers(tiers)
}

func (d *Download) trackerTotals() (seeders, leechers int) {
	return d.Trk.Totals()
}

func (d *Download) trackerErrors() map[string]string {
	m := make(map[string]string)
	d.Trk.Errors.Range(func(url string, msg string) bool {
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
