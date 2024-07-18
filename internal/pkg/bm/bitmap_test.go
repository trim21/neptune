// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bm_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/bm"
)

func TestBitmap(t *testing.T) {
	b := bm.New(10)
	b.Fill()
	require.True(t, b.Contains(9))
	require.False(t, b.Contains(10))
}
