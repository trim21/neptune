// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"neptune/internal/core"
	"neptune/internal/metainfo"
	"neptune/internal/web/jsonrpc"
)

type setSpeedLimitRequest struct {
	InfoHash      string `description:"torrent file hash"                            json:"info_hash"      required:"true"`
	DownloadLimit int64  `description:"download speed limit in bytes/s, 0=unlimited" json:"download_limit"`
	UploadLimit   int64  `description:"upload speed limit in bytes/s, 0=unlimited"   json:"upload_limit"`
}

type setSpeedLimitResponse struct{}

func setSpeedLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*setSpeedLimitRequest, setSpeedLimitResponse](
		func(ctx context.Context, req *setSpeedLimitRequest, res *setSpeedLimitResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			err = c.SetSpeedLimit(metainfo.Hash(raw), req.DownloadLimit, req.UploadLimit)
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to set speed limit"))
			}

			return nil
		},
	)
	u.SetName("torrent.set_speed_limit")
	h.Add(u)
}

type setGlobalSpeedLimitRequest struct {
	DownloadLimit int64 `description:"global download speed limit in bytes/s, 0=no change, -1=unlimited" json:"download_limit"`
	UploadLimit   int64 `description:"global upload speed limit in bytes/s, 0=no change, -1=unlimited"   json:"upload_limit"`
}

type setGlobalSpeedLimitResponse struct{}

func setGlobalSpeedLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*setGlobalSpeedLimitRequest, setGlobalSpeedLimitResponse](
		func(ctx context.Context, req *setGlobalSpeedLimitRequest, res *setGlobalSpeedLimitResponse) error {
			c.SetGlobalSpeedLimit(req.DownloadLimit, req.UploadLimit)
			return nil
		},
	)
	u.SetName("system.set_speed_limit")
	h.Add(u)
}
