// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//nolint:dupl
package web

import (
	"context"

	"github.com/swaggest/usecase"

	"neptune/internal/client"
	"neptune/internal/web/jsonrpc"
)

// client.set_recheck_on_complete
//
// When enabled, every download that completes (all pieces obtained) will be
// re-hash-checked before entering the Seeding state. Pieces that fail the
// check go back to Downloading so they can be re-fetched.

type setRecheckOnCompleteRequest struct {
	Enabled bool `description:"whether to recheck on download completion" json:"enabled"`
}

type setRecheckOnCompleteResponse struct{}

func setRecheckOnComplete(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setRecheckOnCompleteRequest, res *setRecheckOnCompleteResponse) error {
			c.SetRecheckOnComplete(req.Enabled)
			return nil
		},
	)
	u.SetName("client.set_recheck_on_complete")
	h.Add(u)
}

// client.get_recheck_on_complete

type getRecheckOnCompleteRequest struct{}

type getRecheckOnCompleteResponse struct {
	Enabled bool `json:"enabled"`
}

func getRecheckOnComplete(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getRecheckOnCompleteRequest, res *getRecheckOnCompleteResponse) error {
			res.Enabled = c.GetRecheckOnComplete()
			return nil
		},
	)
	u.SetName("client.get_recheck_on_complete")
	h.Add(u)
}
