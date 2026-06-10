// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"

	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"neptune/internal/core"
	"neptune/internal/web/jsonrpc"
)

// torrent.add_tracker

type addTrackerRequest struct {
	InfoHash   string `description:"torrent file hash"                               json:"info_hash" required:"true"`
	TrackerURL string `description:"tracker announce URL"                            json:"url"       required:"true"`
	Tier       int    `description:"tier index, appends to new tier if out of range" json:"tier"`
}

type addTrackerResponse struct{}

func addTracker(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *addTrackerRequest, res *addTrackerResponse) error {
			ih, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			if err := c.AddTracker(ih, req.TrackerURL, req.Tier); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to add tracker"))
			}

			return nil
		},
	)
	u.SetName("torrent.add_tracker")
	h.Add(u)
}

// torrent.remove_tracker

type removeTrackerRequest struct {
	InfoHash   string `description:"torrent file hash"              json:"info_hash" required:"true"`
	TrackerURL string `description:"tracker announce URL to remove" json:"url"       required:"true"`
}

type removeTrackerResponse struct{}

func removeTracker(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *removeTrackerRequest, res *removeTrackerResponse) error {
			ih, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			if err := c.RemoveTracker(ih, req.TrackerURL); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to remove tracker"))
			}

			return nil
		},
	)
	u.SetName("torrent.remove_tracker")
	h.Add(u)
}

// torrent.replace_trackers

type replaceTrackersRequest struct {
	InfoHash     string            `description:"torrent file hash"                         json:"info_hash"    required:"true"`
	Replacements map[string]string `description:"map of old tracker URL to new tracker URL" json:"replacements" required:"true"`
}

type replaceTrackersResponse struct{}

func replaceTrackers(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *replaceTrackersRequest, res *replaceTrackersResponse) error {
			ih, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			if err := c.ReplaceTrackers(ih, req.Replacements); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to replace trackers"))
			}

			return nil
		},
	)
	u.SetName("torrent.replace_trackers")
	h.Add(u)
}
