// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package bep40_test

import (
	"encoding/hex"
	"net/netip"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"neptune/internal/bep40"
	"neptune/internal/pkg/crc32c"
)

func TestBep40Priority4(t *testing.T) {
	require.EqualValues(t, 0xec2d7224, crc32c.Sum(lo.Must(hex.DecodeString("624C14007BD50000"))))
	require.EqualValues(t, 0x99568189, crc32c.Sum(lo.Must(hex.DecodeString("7BD5200A7BD520EA"))))

	require.EqualValues(t, 0xec2d7224, bep40.Priority4(
		netip.MustParseAddrPort("123.213.32.10:0"),
		netip.MustParseAddrPort("98.76.54.32:0"),
	))

	require.EqualValues(t, 0xec2d7224, bep40.Priority4(
		netip.MustParseAddrPort("98.76.54.32:0"),
		netip.MustParseAddrPort("123.213.32.10:0"),
	))
	require.EqualValues(t, 0x99568189, bep40.Priority4(
		netip.MustParseAddrPort("123.213.32.10:0"),
		netip.MustParseAddrPort("123.213.32.234:0"),
	))

	require.EqualValues(t, 0x2b41d456, bep40.Priority4(
		netip.MustParseAddrPort("206.248.98.111:0"),
		netip.MustParseAddrPort("142.147.89.224:0"),
	))
}

func TestV6(t *testing.T) {
	require.Equal(t, uint32(0xfbd26e29), bep40.Priority6(
		netip.MustParseAddrPort("[2015:7693:6cd9:a56a:e47f:7101:483e:800a]:0"),
		netip.MustParseAddrPort("[b1fa:9ff2:fbdc:23b9:3618:332c:216c:5b4a]:0"),
	))
}
