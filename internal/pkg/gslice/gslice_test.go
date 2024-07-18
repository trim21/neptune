// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gslice_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/gslice"
)

func TestRemove(t *testing.T) {
	require.Equal(t, []int{1, 2, 3}, gslice.Remove([]int{1, 2, 3}, 0))
	require.Equal(t, []int{1, 2, 3}, gslice.Remove([]int{1, 2, 3, 4}, 4))
	require.Equal(t, []int{1, 2, 3}, gslice.Remove([]int{1, 0, 2, 3}, 0))
}
