// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package random_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/random"
)

func TestSecureRandomString(t *testing.T) {
	t.Parallel()
	for range 300 {
		s := random.URLSafeStr(64)
		require.Len(t, s, 64)
	}
}

func TestBias(t *testing.T) {
	t.Parallel()
	const sLen = 33
	const loop = 100000

	counts := make(map[rune]int)
	var count int64

	for range loop {
		s := random.URLSafeStr(sLen)
		require.Len(t, s, sLen)
		for _, b := range s {
			counts[b]++
			count++
		}
	}

	require.Len(t, counts, 64)

	avg := float64(count) / float64(len(counts))
	for k, n := range counts {
		diff := float64(n) / avg
		if diff < 0.97 || diff > 1.03 {
			t.Errorf("Bias on '%c': expected average %f, got %d", k, avg, n)
		}
	}
}

func TestDeriveKey(t *testing.T) {
	t.Parallel()

	// Same inputs produce same output
	k1 := random.DeriveKey("my-seed", "abc123", 16)
	k2 := random.DeriveKey("my-seed", "abc123", 16)
	require.Equal(t, k1, k2)
	require.Len(t, k1, 16)

	// Different seed produces different output
	k3 := random.DeriveKey("other-seed", "abc123", 16)
	require.NotEqual(t, k1, k3)

	// Different infoHash produces different output
	k4 := random.DeriveKey("my-seed", "xyz789", 16)
	require.NotEqual(t, k1, k4)

	// Different length
	k5 := random.DeriveKey("my-seed", "abc123", 32)
	require.Len(t, k5, 32)
}

func TestDeriveKeyDeterminism(t *testing.T) {
	t.Parallel()

	// Make sure it's stable across calls
	expected := random.DeriveKey("seed", "hash", 16)
	for range 100 {
		require.Equal(t, expected, random.DeriveKey("seed", "hash", 16))
	}
}

func TestUint64(t *testing.T) {
	t.Parallel()
	seen := make(map[uint64]bool)
	for range 1000 {
		v := random.Uint64()
		require.False(t, seen[v], "duplicate uint64 after %d iterations", len(seen))
		seen[v] = true
	}
}

func TestInt64N(t *testing.T) {
	t.Parallel()
	for range 1000 {
		n := int64(1 + random.Uint64()%100)
		v := random.Int64N(n)
		require.GreaterOrEqual(t, v, int64(0))
		require.Less(t, v, n)
	}
}

func TestInt64NPowerOfTwo(t *testing.T) {
	t.Parallel()
	for range 1000 {
		v := random.Int64N(64)
		require.GreaterOrEqual(t, v, int64(0))
		require.Less(t, v, int64(64))
	}
}
