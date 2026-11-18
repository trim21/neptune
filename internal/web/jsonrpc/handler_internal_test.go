// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package jsonrpc

import (
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

func TestBodyPresize(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		limit         int64
		want          int
	}{
		{"unknown length", -1, 50 << 20, 0},
		{"empty body", 0, 50 << 20, 0},
		{"small body", 128, 50 << 20, 128},
		{"declared length above cap", 50 << 20, 50 << 20, maxPreallocate},
		{"limit below declared length", 4096, 100, 101},
		{"no limit", 4096, 0, 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, bodyPresize(tt.contentLength, tt.limit))
		})
	}
}

func TestReadAllLimitedLargeBody(t *testing.T) {
	body := strings.Repeat("a", 2<<20)

	buf, err := readAllLimited(strings.NewReader(body), int64(len(body)), 50<<20)
	require.NoError(t, err)
	require.Equal(t, body, string(buf.B))
	rpcPool.Put(buf)
}
