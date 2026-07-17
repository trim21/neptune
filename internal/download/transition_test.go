// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/piece_store"
)

func TestValidTransition(t *testing.T) {
	states := []State{
		Downloading,
		PendingDownloading,
		Seeding,
		Checking,
		Stopped,
		Moving,
		Error,
	}
	valid := map[State]map[State]bool{
		Stopped: {
			Downloading: true,
			Seeding:     true,
			Checking:    true,
			Moving:      true,
		},
		Downloading: {
			Stopped:            true,
			Seeding:            true,
			Error:              true,
			Checking:           true,
			Moving:             true,
			PendingDownloading: true,
		},
		PendingDownloading: {
			Downloading: true,
			Seeding:     true,
			Stopped:     true,
			Checking:    true,
			Error:       true,
		},
		Seeding: {
			Stopped:  true,
			Error:    true,
			Checking: true,
			Moving:   true,
		},
		Checking: {
			Downloading: true,
			Seeding:     true,
			Error:       true,
		},
		Moving: {
			Downloading: true,
			Seeding:     true,
			Stopped:     true,
			Error:       true,
		},
		Error: {
			Checking: true,
		},
	}

	for _, from := range states {
		for _, to := range states {
			t.Run(from.String()+"_to_"+to.String(), func(t *testing.T) {
				require.Equal(t, valid[from][to], validTransition(from, to))
			})
		}
	}
}

func TestTransitionResult(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)

	result, err := d.transition(PendingDownloading)
	require.NoError(t, err)
	require.Equal(t, Downloading, result.from)
	require.Equal(t, PendingDownloading, result.to)
	require.True(t, result.changed)
	require.Equal(t, PendingDownloading, d.GetState())

	result, err = d.transition(PendingDownloading)
	require.NoError(t, err)
	require.Equal(t, PendingDownloading, result.from)
	require.Equal(t, PendingDownloading, result.to)
	require.False(t, result.changed)
}

func TestExclusiveStateTransitionIsNotIdempotent(t *testing.T) {
	for _, state := range []State{Checking, Moving} {
		t.Run(state.String(), func(t *testing.T) {
			d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
			d.state.Store(uint32(state))

			result, err := d.transition(state)
			require.Equal(t, state, result.from)
			require.Equal(t, state, result.to)
			require.False(t, result.changed)

			var transitionErr *TransitionError
			require.ErrorAs(t, err, &transitionErr)
			require.Equal(t, state, transitionErr.From)
			require.Equal(t, state, transitionErr.To)
		})
	}
}

func TestStartHookFiresOnlyOnStateChange(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	_, err := d.transition(Stopped)
	require.NoError(t, err)

	hookOutput := filepath.Join(t.TempDir(), "started")
	d.session.Config.App.Hook.OnDownloadStarted = fmt.Sprintf("printf x >> %q", hookOutput)
	expectedHookOutput := []byte("x")

	require.NoError(t, d.Start())
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(hookOutput)
		return readErr == nil && string(data) == string(expectedHookOutput)
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, d.Start())
	require.Never(t, func() bool {
		data, readErr := os.ReadFile(hookOutput)
		return readErr == nil && len(data) > len(expectedHookOutput)
	}, 200*time.Millisecond, 10*time.Millisecond)
	data, err := os.ReadFile(hookOutput)
	require.NoError(t, err)
	require.Equal(t, expectedHookOutput, data)
}
