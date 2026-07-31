// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
)

// TestRunHashCheckFailureReleasesCompletedOnce is a regression test: when a
// completion recheck (recheckAfterComplete) fails during initCheck, the
// completedOnce guard must be released. Otherwise a later completion via
// checkDone is blocked forever by the CompareAndSwap.
func TestRunHashCheckFailureReleasesCompletedOnce(t *testing.T) {
	d := newTestDownload(t, 2, 4, piece_store.NewMemStore)

	// d.s.basePath is empty in the test harness, so initCheck fails in
	// CheckExistingFiles (os.MkdirAll("")).
	d.completedOnce.Store(true)

	d.runHashCheck(nil)

	require.Eventually(t, func() bool {
		return !d.completedOnce.Load() && d.ErrorMsg() != ""
	}, 5*time.Second, 5*time.Millisecond, "completedOnce must be released after initCheck failure")
}
