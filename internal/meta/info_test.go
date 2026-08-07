// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"neptune/internal/metainfo"
)

// buildMetaInfo marshals an Info into a MetaInfo with the given piece length.
func buildMetaInfo(pieceLength int64) metainfo.MetaInfo {
	infoBytes, err := bencode.Marshal(metainfo.Info{
		Name:        "test",
		PieceLength: pieceLength,
		Length:      100,
		Pieces:      make([]byte, 20), // 1 piece
	})
	if err != nil {
		panic(err)
	}
	return metainfo.MetaInfo{InfoBytes: infoBytes}
}

// FromTorrent must reject zero or negative piece lengths instead of dividing
// by zero while validating the piece count. A malicious torrent with
// `piece length=0` used to panic the whole process (integer divide by zero).
func TestFromTorrentRejectsInvalidPieceLength(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		length int64
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := FromTorrent(buildMetaInfo(tc.length))
			require.ErrorIs(t, err, ErrInvalidLength)
		})
	}
}

// Sanity check: the same torrent with a valid piece length parses fine.
func TestFromTorrentAcceptsValidPieceLength(t *testing.T) {
	t.Parallel()

	info, err := FromTorrent(buildMetaInfo(100))
	require.NoError(t, err)
	require.Equal(t, int64(100), info.PieceLength)
	require.Equal(t, uint32(1), info.NumPieces)
	require.Equal(t, int64(100), info.LastPieceSize)
}
