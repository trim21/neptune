// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"github.com/swaggest/usecase"

	"neptune/internal/client"
	"neptune/internal/metainfo"
	"neptune/internal/web/jsonrpc"
)

type reannounceRequest struct {
	InfoHash string `description:"torrent file hash" json:"info_hash" required:"true"`
}

type reannounceResponse struct{}

func reannounceTorrent(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *reannounceRequest, res *reannounceResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			return c.Reannounce(metainfo.Hash(raw))
		},
	)
	u.SetName("torrent.reannounce")
	h.Add(u)
}
