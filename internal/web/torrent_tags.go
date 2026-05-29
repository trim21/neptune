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

type addTagsRequest struct {
	InfoHash string   `description:"torrent file hash" json:"info_hash" required:"true"`
	Tags     []string `description:"tags to add"       json:"tags"      required:"true"`
}

type addTagsResponse struct{}

func addTags(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *addTagsRequest, res *addTagsResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			return c.AddTags(metainfo.Hash(raw), req.Tags)
		},
	)
	u.SetName("torrent.add_tags")
	h.Add(u)
}

type removeTagsRequest struct {
	InfoHash string   `description:"torrent file hash" json:"info_hash" required:"true"`
	Tags     []string `description:"tags to remove"    json:"tags"      required:"true"`
}

type removeTagsResponse struct{}

func removeTags(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *removeTagsRequest, res *removeTagsResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			return c.RemoveTags(metainfo.Hash(raw), req.Tags)
		},
	)
	u.SetName("torrent.remove_tags")
	h.Add(u)
}
