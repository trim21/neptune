// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"go.uber.org/atomic"
)

// PeerContext holds the shared download state that a peer needs.
// It is always valid (never nil). Fields that are stored on PiecePicker
// (info, completedBm, debug, etc.) are accessed via Picker().
type PeerContext struct {
	picker        *atomic.Pointer[PiecePicker]
	isDownloading func() bool
}

// Picker returns the current piece picker (may be nil when seeding).
func (c *PeerContext) Picker() *PiecePicker {
	return c.picker.Load()
}

// IsDownloading returns whether the download is in downloading state.
func (c *PeerContext) IsDownloading() bool {
	return c.isDownloading()
}

// newPeerContext creates a PeerContext from the download's state.
func (d *Download) newPeerContext() *PeerContext {
	return &PeerContext{
		picker:        &d.picker,
		isDownloading: func() bool { return d.HasState(Downloading) },
	}
}
