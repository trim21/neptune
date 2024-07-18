// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package proto_test

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/proto"
)

func TestSendPiece(t *testing.T) {
	var b bytes.Buffer

	require.NoError(t, proto.SendPiece(&b, &proto.ChunkResponse{
		Data:       []byte("hello world"),
		Begin:      20,
		PieceIndex: 5,
	}))

	require.Equal(t, "\x00\x00\x00\x14\x07\x00\x00\x00\x05\x00\x00\x00\x14hello world", b.String())
}
