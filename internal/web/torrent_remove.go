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

type removeTorrentRequest struct {
	InfoHash   string `json:"info_hash" description:"torrent file hash" required:"true"`
	DeleteData bool   `json:"delete_data" description:"delete torrent data" required:"false"`
}

type removeTorrentResponse struct {
	core.TorrentList
}

func removeTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*removeTorrentRequest, removeTorrentResponse](
		func(ctx context.Context, req *removeTorrentRequest, res *removeTorrentResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			h, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			return c.RemoveTorrent(metainfo.Hash(h), req.DeleteData)
		},
	)
	u.SetName("torrent.remove")
	h.Add(u)
}
