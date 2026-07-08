// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"context"
	"crypto/sha1"
	"encoding/hex"

	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"neptune/internal/client"
	"neptune/internal/download"
	"neptune/internal/metainfo"
	"neptune/internal/web/jsonrpc"
)

// client.set_piece_pick_strategy

type setPiecePickStrategyRequest struct {
	Strategy string `description:"piece pick strategy: 'rarest-first' or 'sequential'" json:"strategy" required:"true"`
}

type setPiecePickStrategyResponse struct{}

func setPiecePickStrategy(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *setPiecePickStrategyRequest, res *setPiecePickStrategyResponse) error {
			st, err := download.PiecePickStrategyFromString(req.Strategy)
			if err != nil {
				return err
			}
			c.SetDefaultPiecePickStrategy(st)
			return nil
		},
	)
	u.SetName("client.set_piece_pick_strategy")
	h.Add(u)
}

// client.get_piece_pick_strategy

type getPiecePickStrategyRequest struct{}

type getPiecePickStrategyResponse struct {
	Strategy string `json:"strategy"`
}

func getPiecePickStrategy(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *getPiecePickStrategyRequest, res *getPiecePickStrategyResponse) error {
			res.Strategy = c.GetDefaultPiecePickStrategy().String()
			return nil
		},
	)
	u.SetName("client.get_piece_pick_strategy")
	h.Add(u)
}

// torrent.set_piece_pick_strategy

type torrentSetPiecePickStrategyRequest struct {
	InfoHash string `description:"torrent file hash"                                   json:"info_hash" required:"true"`
	Strategy string `description:"piece pick strategy: 'rarest-first' or 'sequential'" json:"strategy"  required:"true"`
}

type torrentSetPiecePickStrategyResponse struct{}

func torrentSetPiecePickStrategy(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *torrentSetPiecePickStrategyRequest, res *torrentSetPiecePickStrategyResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}
			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			st, err := download.PiecePickStrategyFromString(req.Strategy)
			if err != nil {
				return err
			}

			err = c.SetTorrentPiecePickStrategy(metainfo.Hash(raw), st)
			if err != nil {
				return err
			}
			return nil
		},
	)
	u.SetName("torrent.set_piece_pick_strategy")
	h.Add(u)
}

// torrent.get_piece_pick_strategy

type torrentGetPiecePickStrategyRequest struct {
	InfoHash string `description:"torrent file hash" json:"info_hash" required:"true"`
}

type torrentGetPiecePickStrategyResponse struct {
	Strategy string `json:"strategy"`
}

func torrentGetPiecePickStrategy(h *jsonrpc.Handler, c *client.Client) {
	u := usecase.NewInteractor(
		func(ctx context.Context, req *torrentGetPiecePickStrategyRequest, res *torrentGetPiecePickStrategyResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}
			raw, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			st, err := c.GetTorrentPiecePickStrategy(metainfo.Hash(raw))
			if err != nil {
				return CodeError(1, errgo.Wrap(err, "failed to get piece pick strategy"))
			}
			res.Strategy = st.String()
			return nil
		},
	)
	u.SetName("torrent.get_piece_pick_strategy")
	h.Add(u)
}
