// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"neptune/internal/client"
	"neptune/internal/metainfo"
	"neptune/internal/web/jsonrpc"
)

type setFilePriorityRequest struct {
	InfoHash string `description:"torrent file hash"                 json:"info_hash" required:"true"`
	FileIDs  []int  `description:"indices of files to set"           json:"file_ids"  required:"true"`
	Priority int    `description:"file priority, 0=skip, 1=download" json:"priority"  required:"true"`
}

type setFilePriorityResponse struct{}

func setFilePriority(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setFilePriorityRequest, res *setFilePriorityResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			err = c.SetFilePriority(metainfo.Hash(raw), req.FileIDs, req.Priority)
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to set file priority"))
			}

			return nil
		},
	)
	u.SetName("torrent.set_file_priority")
	h.Add(u)
}
