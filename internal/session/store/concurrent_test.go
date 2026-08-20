// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConcurrentUpsert is a regression test for SQLITE_BUSY during shutdown:
// Client.Shutdown saves every download concurrently (up to 5 at a time), and
// before the single-connection refactor each save opened its own connection
// whose `PRAGMA journal_mode=WAL` raced with the others and failed with
// "database is locked".
func TestConcurrentUpsert(t *testing.T) {
	s := openTestStore(t)

	errCh := make(chan error, 50*5)
	for round := range 50 {
		var wg sync.WaitGroup
		for i := range 5 {
			wg.Add(1)
			go func(hash string) {
				defer wg.Done()
				errCh <- s.Upsert(&Resume{InfoHash: hash, BasePath: "/data"})
			}(fmt.Sprintf("hash-%d-%d", round, i))
		}
		wg.Wait()
	}
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 50*5, n)
}
