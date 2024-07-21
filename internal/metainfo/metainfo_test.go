// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package metainfo

import (
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"
)

func testFile(t *testing.T, filename string) {
	mi, err := LoadFromFile(filename)
	require.NoError(t, err)
	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)

	if len(info.Files) == 1 {
		t.Logf("Single file: %s (length: %d)\n", info.BestName(), info.Files[0].Length)
	} else {
		t.Logf("Multiple files: %s\n", info.BestName())
		for _, f := range info.Files {
			t.Logf(" - %s (length: %d)\n", path.Join(f.Path...), f.Length)
		}
	}

	for _, group := range mi.AnnounceList {
		for _, tracker := range group {
			t.Logf("Tracker: %s\n", tracker)
		}
	}

	b, err := bencode.Marshal(&info)
	require.NoError(t, err)
	require.EqualValues(t, string(b), string(mi.InfoBytes))
}

func TestFile(t *testing.T) {
	testFile(t, "testdata/archlinux-2011.08.19-netinstall-i686.iso.torrent")
	testFile(t, "testdata/continuum.torrent")
	testFile(t, "testdata/23516C72685E8DB0C8F15553382A927F185C4F01.torrent")
	testFile(t, "testdata/trackerless.torrent")
}

func testUnmarshal(t *testing.T, input string, expected *MetaInfo) {
	var actual MetaInfo
	err := bencode.Unmarshal([]byte(input), &actual)
	if expected == nil {
		require.Error(t, err)
		return
	}
	require.NoError(t, err)
	require.EqualValues(t, *expected, actual)
}

func TestUnmarshal(t *testing.T) {
	testUnmarshal(t, `de`, &MetaInfo{})
	testUnmarshal(t, `d4:infoe`, nil)
	testUnmarshal(t, `d4:infoabce`, nil)
	testUnmarshal(t, `d4:infodee`, &MetaInfo{InfoBytes: []byte("de")})
}

// https://github.com/anacrolix/torrent/issues/247
//
// The decoder buffer wasn't cleared before starting the next dict item after
// a syntax error on a field with the ignore_unmarshal_type_error tag.
func TestStringCreationDate(t *testing.T) {
	var mi MetaInfo
	require.NoError(t, bencode.Unmarshal([]byte("d13:creation date23:29.03.2018 22:18:14 UTC4:infodee"), &mi))
}

// See https://github.com/anacrolix/torrent/issues/843.
func TestUnmarshalEmptyStringNodes(t *testing.T) {
	var mi MetaInfo
	err := bencode.Unmarshal([]byte("d5:nodes0:e"), &mi)
	require.NoError(t, err)
}
