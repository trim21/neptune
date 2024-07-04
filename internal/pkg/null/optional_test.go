// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package null_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/null"
)

func TestNilUint8(t *testing.T) {
	t.Parallel()

	require.Nil(t, null.NilUint8(0))

	require.Equal(t, uint8(3), *null.NilUint8(3))
}

func TestNilUint16(t *testing.T) {
	t.Parallel()

	require.Nil(t, null.NilUint16(0))

	require.Equal(t, uint16(3), *null.NilUint16(3))
}

func TestNilString(t *testing.T) {
	t.Parallel()

	require.Nil(t, null.NilString(""))

	require.Equal(t, "s", *null.NilString("s"))
}
