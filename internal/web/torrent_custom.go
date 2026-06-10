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

type setCustomRequest struct {
	InfoHash string `description:"torrent file hash" json:"info_hash" required:"true"`
	Key      string `description:"custom key"        json:"key"       required:"true"`
	Value    string `description:"custom value"      json:"value"     required:"true"`
}

type setCustomResponse struct{}

func setCustom(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setCustomRequest, res *setCustomResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			if err := c.SetCustom(metainfo.Hash(raw), req.Key, req.Value); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to set custom"))
			}

			return nil
		},
	)
	u.SetName("torrent.custom.set")
	h.Add(u)
}

type updateCustomRequest struct {
	Custom   map[string]string `description:"custom to update"  json:"custom"    required:"true"`
	InfoHash string            `description:"torrent file hash" json:"info_hash" required:"true"`
}

type updateCustomResponse struct{}

func updateCustom(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *updateCustomRequest, res *updateCustomResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			if err := c.UpdateCustom(metainfo.Hash(raw), req.Custom); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to update custom"))
			}

			return nil
		},
	)
	u.SetName("torrent.custom.update")
	h.Add(u)
}

type delCustomRequest struct {
	InfoHash string `description:"torrent file hash"    json:"info_hash" required:"true"`
	Key      string `description:"custom key to delete" json:"key"       required:"true"`
}

type delCustomResponse struct{}

func delCustom(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *delCustomRequest, res *delCustomResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			if err := c.DelCustom(metainfo.Hash(raw), req.Key); err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to delete custom"))
			}

			return nil
		},
	)
	u.SetName("torrent.custom.del")
	h.Add(u)
}
