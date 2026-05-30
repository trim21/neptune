// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"github.com/swaggest/usecase"

	"neptune/internal/core"
	"neptune/internal/metainfo"
	"neptune/internal/web/jsonrpc"
)

type stopTorrentRequest struct {
	InfoHash string `description:"torrent file hash" json:"info_hash" required:"true"`
}

type stopTorrentResponse struct{}

func stopTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *stopTorrentRequest, res *stopTorrentResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			return c.StopTorrent(metainfo.Hash(raw))
		},
	)
	u.SetName("torrent.stop")
	h.Add(u)
}
