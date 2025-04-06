// Copyright 2024 trim21 <trim21.me@gmail.com>
// Copyright https://github.com/anacrolix
// SPDX-License-Identifier: MPL-2.0
// https://github.com/anacrolix/torrent/blob/v1.56.1/LICENSE

package metainfo

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"
)

func TestMarshalInfo(t *testing.T) {
	var info Info
	b, err := bencode.Marshal(info)
	require.NoError(t, err)
	require.Equal(t, "d4:name0:12:piece lengthi0e6:pieces0:e", string(b))
}
