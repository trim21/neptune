// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"

	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"neptune/internal/client"
	"neptune/internal/web/jsonrpc"
)

// torrent.set_queue_weight

type setQueueWeightRequest struct {
	InfoHash string `description:"torrent file hash"                                 json:"info_hash" required:"true"`
	Weight   int    `description:"queue priority weight, higher means more priority" json:"weight"`
}

type setQueueWeightResponse struct{}

func setQueueWeight(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setQueueWeightRequest, res *setQueueWeightResponse) error {
			h, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			err = c.SetQueueWeight(h, req.Weight)
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to set queue weight"))
			}
			return nil
		},
	)
	u.SetName("torrent.set_queue_weight")
	h.Add(u)
}
