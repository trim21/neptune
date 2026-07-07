//go:build release
// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

// release: pieceStore is the concrete pointer type — direct method call, zero interface overhead.
type pieceStore = *fileStoreWriter
