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

// client.set_torrent_connection_limit

type setTorrentConnectionLimitRequest struct {
	Limit uint16 `description:"max connections per torrent" json:"limit"`
}

type setTorrentConnectionLimitResponse struct{}

func setTorrentConnectionLimit(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setTorrentConnectionLimitRequest, res *setTorrentConnectionLimitResponse) error {
			c.SetTorrentConnectionLimit(req.Limit)
			return nil
		},
	)
	u.SetName("client.set_torrent_connection_limit")
	h.Add(u)
}

// client.get_torrent_connection_limit

type getTorrentConnectionLimitRequest struct{}

type getTorrentConnectionLimitResponse struct {
	Limit uint16 `json:"limit"`
}

func getTorrentConnectionLimit(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getTorrentConnectionLimitRequest, res *getTorrentConnectionLimitResponse) error {
			res.Limit = c.GetTorrentConnectionLimit()
			return nil
		},
	)
	u.SetName("client.get_torrent_connection_limit")
	h.Add(u)
}
