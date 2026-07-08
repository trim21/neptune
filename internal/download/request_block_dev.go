// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import "time"

// enqueueBlockDelay widens the race window between markAsRequesting
// and EnqueueBlock so that FuzzStaleRequest can catch orphaned blocks.
func enqueueBlockDelay() {
	time.Sleep(time.Millisecond)
}
