// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package download

// Peer is a concrete pointer in release mode — zero interface dispatch overhead.
type Peer = *peerImpl
