// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package jsonrpc

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadAllLimited(t *testing.T) {
	t.Run("small body", func(t *testing.T) {
		body := `{"jsonrpc":"2.0","id":1,"method":"system.ping"}`
		buf, err := readAllLimited(strings.NewReader(body), int64(len(body)), 50<<20)
		require.NoError(t, err)
		require.Equal(t, body, string(buf.B))
		rpcPool.Put(buf)
	})

	t.Run("exactly at limit", func(t *testing.T) {
		body := strings.Repeat("a", 16)
		buf, err := readAllLimited(strings.NewReader(body), 0, int64(len(body)))
		require.NoError(t, err)
		require.Equal(t, body, string(buf.B))
		rpcPool.Put(buf)
	})

	t.Run("over limit", func(t *testing.T) {
		body := strings.Repeat("a", 17)
		buf, err := readAllLimited(strings.NewReader(body), 0, 16)
		require.ErrorIs(t, err, errBodyTooLarge)
		rpcPool.Put(buf)
	})

	t.Run("no limit", func(t *testing.T) {
		body := strings.Repeat("a", 100)
		buf, err := readAllLimited(strings.NewReader(body), 0, 0)
		require.NoError(t, err)
		require.Equal(t, body, string(buf.B))
		rpcPool.Put(buf)
	})

	t.Run("content length above limit", func(t *testing.T) {
		body := strings.Repeat("a", 32)
		buf, err := readAllLimited(strings.NewReader(body), 1<<20, 16)
		require.ErrorIs(t, err, errBodyTooLarge)
		rpcPool.Put(buf)
	})
}

func TestReadAllLimitedDoesNotAllocateLimit(t *testing.T) {
	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)
	buf, err := readAllLimited(strings.NewReader(`{"jsonrpc":"2.0"}`), 0, 50<<20)
	runtime.ReadMemStats(&after)

	require.NoError(t, err)
	require.JSONEq(t, `{"jsonrpc":"2.0"}`, string(buf.B))
	rpcPool.Put(buf)

	grown := after.TotalAlloc - before.TotalAlloc
	require.Less(t, grown, uint64(1<<20), "a small body must not allocate the 50 MiB body limit")
}

func TestReadAllLimitedDoesNotTrustContentLength(t *testing.T) {
	body := "ab"

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	buf, err := readAllLimited(strings.NewReader(body), 50<<20, 50<<20)
	runtime.ReadMemStats(&after)

	require.NoError(t, err)
	require.Equal(t, body, string(buf.B))
	rpcPool.Put(buf)

	grown := after.TotalAlloc - before.TotalAlloc
	require.Less(t, grown, uint64(maxPreallocate*2), "a declared length alone must not allocate the whole body")
}

func TestReadAllLimitedLargeBody(t *testing.T) {
	body := strings.Repeat("a", 2<<20)

	buf, err := readAllLimited(strings.NewReader(body), int64(len(body)), 50<<20)
	require.NoError(t, err)
	require.Equal(t, body, string(buf.B))
	rpcPool.Put(buf)
}
