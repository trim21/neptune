// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
)

func TestSetFilePriorityDoesNotDeadlockWhileSavingResume(t *testing.T) {
	d := newTestDownload(t, 2, 4, piece_store.NewMemStore)
	d.session.ResumePath = t.TempDir()
	d.selectedFilesSet = bm.New(uint32(len(d.info.Files)))
	d.selectedFilesSet.Fill()

	done := make(chan error, 1)
	go func() {
		done <- d.SetFilePriority([]int{0}, 1)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("SetFilePriority deadlocked while saving resume data")
	}
}
