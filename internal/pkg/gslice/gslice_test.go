// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gslice_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/gslice"
)

func TestMax(t *testing.T) {
	i, m := gslice.Max([]int{1, 3, 2})
	require.Equal(t, 3, m)
	require.Equal(t, 1, i)
}
