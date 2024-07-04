// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package random_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/random"
)

func TestSecureRandomString(t *testing.T) {
	t.Parallel()
	for i := 0; i < 300; i++ {
		s := random.UrlSafeStr(64)
		require.Equal(t, 64, len(s))
	}
}

func TestBias(t *testing.T) {
	t.Parallel()
	const slen = 33
	const loop = 100000

	counts := make(map[rune]int)
	var count int64

	for i := 0; i < loop; i++ {
		s := random.UrlSafeStr(slen)
		require.Equal(t, slen, len(s))
		for _, b := range s {
			counts[b]++
			count++
		}
	}

	require.Equal(t, 64, len(counts))

	avg := float64(count) / float64(len(counts))
	for k, n := range counts {
		diff := float64(n) / avg
		if diff < 0.97 || diff > 1.03 {
			t.Errorf("Bias on '%c': expected average %f, got %d", k, avg, n)
		}
	}
}
