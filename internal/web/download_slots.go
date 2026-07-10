// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"

	"github.com/swaggest/usecase"

	"neptune/internal/client"
	"neptune/internal/web/jsonrpc"
)

// client.set_download_slots

type setDownloadSlotsRequest struct {
	Slots uint16 `description:"max concurrent actively-downloading torrents, 0=unlimited" json:"slots"`
}

type setDownloadSlotsResponse struct{}

func setDownloadSlots(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setDownloadSlotsRequest, res *setDownloadSlotsResponse) error {
			c.SetDownloadSlots(req.Slots)
			return nil
		},
	)
	u.SetName("client.set_download_slots")
	h.Add(u)
}

// client.get_download_slots

type getDownloadSlotsRequest struct{}

type getDownloadSlotsResponse struct {
	Slots uint16 `json:"slots"`
}

func getDownloadSlots(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getDownloadSlotsRequest, res *getDownloadSlotsResponse) error {
			res.Slots = c.GetDownloadSlots()
			return nil
		},
	)
	u.SetName("client.get_download_slots")
	h.Add(u)
}

// client.set_slow_download_speed_threshold

type setSlowDownloadSpeedThresholdRequest struct {
	Threshold int64 `description:"speed in bytes/s below which a download does not consume a slot, 0=disabled" json:"threshold"`
}

type setSlowDownloadSpeedThresholdResponse struct{}

func setSlowDownloadSpeedThreshold(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setSlowDownloadSpeedThresholdRequest, res *setSlowDownloadSpeedThresholdResponse) error {
			c.SetSlowDownloadSpeedThreshold(req.Threshold)
			return nil
		},
	)
	u.SetName("client.set_slow_download_speed_threshold")
	h.Add(u)
}

// client.get_slow_download_speed_threshold

type getSlowDownloadSpeedThresholdRequest struct{}

type getSlowDownloadSpeedThresholdResponse struct {
	Threshold int64 `json:"threshold"`
}

func getSlowDownloadSpeedThreshold(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getSlowDownloadSpeedThresholdRequest, res *getSlowDownloadSpeedThresholdResponse) error {
			res.Threshold = c.GetSlowDownloadSpeedThreshold()
			return nil
		},
	)
	u.SetName("client.get_slow_download_speed_threshold")
	h.Add(u)
}
