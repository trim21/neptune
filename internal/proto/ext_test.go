// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"tyr/internal/pkg/null"
	"tyr/internal/proto"
)

func TestEncodeExtHandshake(t *testing.T) {
	t.Parallel()

	raw, err := bencode.Marshal(proto.ExtHandshake{
		V: null.NewString("tyr 0.0.1"),
		M: proto.ExtM{
			UTPex: null.Uint8{
				Value: 10,
				Set:   true,
			},
		},
		QueueLength: null.NewUint32(20),
	})

	require.NoError(t, err)
	require.Equal(t, `d1:md6:ut_pexi10ee4:reqqi20e1:v9:tyr 0.0.1e`, string(raw))
}
