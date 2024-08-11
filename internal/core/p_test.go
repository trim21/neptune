// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPeerID(t *testing.T) {
	require.Len(t, peerIDPrefix, 8)
}
