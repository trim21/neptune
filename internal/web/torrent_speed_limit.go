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

func checkInfoHash(infoHash string) (metainfo.Hash, error) {
	if len(infoHash) != sha1.Size*2 {
		return metainfo.Hash{}, errInvalidInfoHash
	}

	raw, err := hex.DecodeString(infoHash)
	if err != nil {
		return metainfo.Hash{}, errInvalidInfoHash
	}

	return metainfo.Hash(raw), nil
}

// torrent.set_download_limit

type setDownloadLimitRequest struct {
	InfoHash string `description:"torrent file hash"                              json:"info_hash" required:"true"`
	Limit    int64  `description:"download speed limit in bytes/s, <=0=unlimited" json:"limit"`
}

type setDownloadLimitResponse struct{}

func setDownloadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setDownloadLimitRequest, res *setDownloadLimitResponse) error {
			h, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			err = c.SetDownloadLimit(h, req.Limit)
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to set download limit"))
			}

			return nil
		},
	)
	u.SetName("torrent.set_download_limit")
	h.Add(u)
}

// torrent.set_upload_limit

type setUploadLimitRequest struct {
	InfoHash string `description:"torrent file hash"                            json:"info_hash" required:"true"`
	Limit    int64  `description:"upload speed limit in bytes/s, <=0=unlimited" json:"limit"`
}

type setUploadLimitResponse struct{}

func setUploadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setUploadLimitRequest, res *setUploadLimitResponse) error {
			h, err := checkInfoHash(req.InfoHash)
			if err != nil {
				return err
			}

			err = c.SetUploadLimit(h, req.Limit)
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to set upload limit"))
			}

			return nil
		},
	)
	u.SetName("torrent.set_upload_limit")
	h.Add(u)
}

// client.set_download_limit

type setGlobalDownloadLimitRequest struct {
	Limit int64 `description:"global download speed limit in bytes/s, <=0=unlimited" json:"limit"`
}

type setGlobalDownloadLimitResponse struct{}

func setGlobalDownloadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setGlobalDownloadLimitRequest, res *setGlobalDownloadLimitResponse) error {
			c.SetGlobalDownloadLimit(req.Limit)
			return nil
		},
	)
	u.SetName("client.set_download_limit")
	h.Add(u)
}

// client.set_upload_limit

type setGlobalUploadLimitRequest struct {
	Limit int64 `description:"global upload speed limit in bytes/s, <=0=unlimited" json:"limit"`
}

type setGlobalUploadLimitResponse struct{}

func setGlobalUploadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setGlobalUploadLimitRequest, res *setGlobalUploadLimitResponse) error {
			c.SetGlobalUploadLimit(req.Limit)
			return nil
		},
	)
	u.SetName("client.set_upload_limit")
	h.Add(u)
}

// client.get_download_limit

type getGlobalDownloadLimitRequest struct{}

type getGlobalDownloadLimitResponse struct {
	Limit int64 `json:"limit"`
}

func getGlobalDownloadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getGlobalDownloadLimitRequest, res *getGlobalDownloadLimitResponse) error {
			res.Limit = c.GetGlobalDownloadLimit()
			return nil
		},
	)
	u.SetName("client.get_download_limit")
	h.Add(u)
}

// client.get_upload_limit

type getGlobalUploadLimitRequest struct{}

type getGlobalUploadLimitResponse struct {
	Limit int64 `json:"limit"`
}

func getGlobalUploadLimit(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getGlobalUploadLimitRequest, res *getGlobalUploadLimitResponse) error {
			res.Limit = c.GetGlobalUploadLimit()
			return nil
		},
	)
	u.SetName("client.get_upload_limit")
	h.Add(u)
}
