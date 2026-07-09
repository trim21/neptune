// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
)

// PeerContext holds the read-only download state that a peer needs for
// block scheduling. It decouples peerImpl from *Download so that peer
// scheduling can be tested with a fake context.
// A PeerContext is always valid (never nil).
type PeerContext struct {
	completedBm    *bm.Bitmap
	pickStrategyFn func() PiecePickStrategy
	picker         *atomic.Pointer[PiecePicker]
	chunkDone      *chunkDoneAccessor
	isDownloading  func() bool
	info           meta.Info
	normalChunkLen uint32
	debug          bool
}

// chunkDoneAccessor wraps the chunk.done bitmap with its mutex.
type chunkDoneAccessor struct {
	done *chunkState
}

func (a *chunkDoneAccessor) contains(pi uint32) bool {
	a.done.mu.RLock()
	defer a.done.mu.RUnlock()
	return a.done.done.Contains(pi)
}

// Picker returns the current picker (may be nil when seeding).
func (c *PeerContext) Picker() *PiecePicker {
	return c.picker.Load()
}

// IsDownloading returns whether the download is in downloading state.
func (c *PeerContext) IsDownloading() bool {
	return c.isDownloading()
}

// IsDebug returns whether debug logging is enabled.
func (c *PeerContext) IsDebug() bool {
	return c.debug
}

// ChunkDone returns whether the chunk at the given position has been written to disk.
func (c *PeerContext) ChunkDone(pi uint32) bool {
	return c.chunkDone.contains(pi)
}

// CompletedBm returns the completed piece bitmap.
func (c *PeerContext) CompletedBm() *bm.Bitmap { return c.completedBm }

// Info returns the torrent metadata.
func (c *PeerContext) Info() meta.Info { return c.info }

// NormalChunkLen returns the number of chunks per piece.
func (c *PeerContext) NormalChunkLen() uint32 { return c.normalChunkLen }

// PiecePickStrategy returns the current piece pick strategy.
func (c *PeerContext) PiecePickStrategy() PiecePickStrategy {
	return c.pickStrategyFn()
}

// newPeerContext creates a PeerContext from the download's state.
func (d *Download) newPeerContext() *PeerContext {
	return &PeerContext{
		info:           d.info,
		completedBm:    d.completedBm,
		normalChunkLen: d.normalChunkLen,
		pickStrategyFn: d.GetPiecePickStrategy,
		picker:         &d.picker,
		chunkDone:      &chunkDoneAccessor{done: &d.chunk},
		isDownloading:  func() bool { return d.HasState(Downloading) },
		debug:          d.session.Debug,
	}
}
