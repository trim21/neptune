// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package null_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"tyr/internal/pkg/null"
)

func TestNull_Ptr(t *testing.T) {
	t.Parallel()

	n := null.Int{Set: true, Value: 1}
	require.Equal(t, 1, *n.Ptr())

	n = null.Int{Set: false, Value: 1}
	require.Nil(t, n.Ptr())
}

func TestNull_Default(t *testing.T) {
	t.Parallel()

	n := null.Int{Set: true, Value: 1}
	require.Equal(t, 1, n.Default(10))

	n = null.Int{Set: false, Value: 1}
	require.Equal(t, 10, n.Default(10))
}

func TestNull_Interface(t *testing.T) {
	t.Parallel()

	n := null.Int{Set: true, Value: 1}
	require.EqualValues(t, 1, n.Interface())

	n = null.Int{Set: false, Value: 1}
	require.EqualValues(t, nil, n.Interface())
}

func TestNull_UnmarshalJSON(t *testing.T) {
	t.Parallel()

	var n null.Int
	require.NoError(t, json.Unmarshal([]byte("10"), &n))
	require.EqualValues(t, 10, n.Value)

	n = null.Int{}
	require.NoError(t, json.Unmarshal([]byte(" null "), &n))
	require.False(t, n.Set)
}

func Test_UnmarshalBencode(t *testing.T) {
	t.Parallel()

	var n null.Int
	require.NoError(t, bencode.Unmarshal([]byte("i10e"), &n))
	require.EqualValues(t, 10, n.Value)

	var s struct {
		N null.Int `bencode:"n"`
	}
	require.NoError(t, bencode.Unmarshal([]byte("de"), &s))
	require.False(t, s.N.Set)

	var s2 struct {
		N null.Null[bencode.RawBytes] `bencode:"n"`
	}
	require.NoError(t, bencode.Unmarshal([]byte("de"), &s2))
	require.False(t, s2.N.Set)

	var s3 struct {
		N null.Null[bencode.RawBytes] `bencode:"n"`
	}
	require.NoError(t, bencode.Unmarshal([]byte("d1:ni10ee"), &s3))
	require.True(t, s3.N.Set)
	require.EqualValues(t, bencode.RawBytes("i10e"), s3.N.Value)
}
