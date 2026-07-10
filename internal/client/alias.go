// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import "neptune/internal/download"

// Type aliases for types moved to the download package.
// This keeps existing core code working without prefix changes.
type (
	Download        = download.Download
	Peer            = download.Peer
	State           = download.State
	TransitionError = download.TransitionError
)

// State constants (re-exported from download package).
const (
	Stopped            = download.Stopped
	Downloading        = download.Downloading
	PendingDownloading = download.PendingDownloading
	Seeding            = download.Seeding
	Checking           = download.Checking
	Moving             = download.Moving
	StateError         = download.Error
	UnchokeInterval    = download.UnchokeInterval
)
