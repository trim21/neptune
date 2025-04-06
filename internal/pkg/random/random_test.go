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
