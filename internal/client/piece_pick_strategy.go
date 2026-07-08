// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"neptune/internal/download"
	"neptune/internal/metainfo"
)

// SetDefaultPiecePickStrategy sets the client-level default piece pick strategy.
// New downloads will use this strategy unless overridden per-download.
func (c *Client) SetDefaultPiecePickStrategy(s download.PiecePickStrategy) {
	c.piecePickStrategy.Store(uint32(s))
}

// GetDefaultPiecePickStrategy returns the client-level default piece pick strategy.
func (c *Client) GetDefaultPiecePickStrategy() download.PiecePickStrategy {
	return download.PiecePickStrategy(c.piecePickStrategy.Load())
}

// GetTorrentPiecePickStrategy returns the piece pick strategy for a specific download.
func (c *Client) GetTorrentPiecePickStrategy(h metainfo.Hash) (download.PiecePickStrategy, error) {
	c.m.RLock()
	defer c.m.RUnlock()

	d, ok := c.downloadMap[h]
	if !ok {
		return 0, download.ErrTorrentNotFound
	}
	return d.GetPiecePickStrategy(), nil
}

// SetTorrentPiecePickStrategy sets the piece pick strategy for a specific download.
func (c *Client) SetTorrentPiecePickStrategy(h metainfo.Hash, s download.PiecePickStrategy) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return download.ErrTorrentNotFound
	}
	d.SetPiecePickStrategy(s)
	return nil
}
