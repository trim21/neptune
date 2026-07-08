// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafePathComponent(t *testing.T) {
	t.Parallel()

	// short name passes through unchanged
	require.Equal(t, "short.txt", SafePathComponent("short.txt"))
	require.Equal(t, "hello", SafePathComponent("hello"))

	// exactly at limit passes through
	name255 := strings.Repeat("a", 255)
	require.Equal(t, name255, SafePathComponent(name255))

	// long name with extension gets truncated with hash
	longName := strings.Repeat("\u554a", 100) + ".mp4"
	result := SafePathComponent(longName)
	require.LessOrEqual(t, len(result), 255)
	require.True(t, strings.HasSuffix(result, ".mp4"), "should preserve .mp4 extension, got: %s", result)
	// should contain hash
	require.Contains(t, result, ".")

	// long name without extension
	longNoExt := strings.Repeat("\u4e2d", 100)
	result2 := SafePathComponent(longNoExt)
	require.LessOrEqual(t, len(result2), 255)
	require.Contains(t, result2, ".")

	// Verify different long names produce different results
	a := SafePathComponent(strings.Repeat("a", 300) + ".txt")
	b := SafePathComponent(strings.Repeat("b", 300) + ".txt")
	require.NotEqual(t, a, b)
}

func TestTruncateToValidUTF8(t *testing.T) {
	t.Parallel()

	// short string passes through
	require.Equal(t, "abc", truncateToValidUTF8("abc", 10))

	// truncate at ASCII boundary
	require.Equal(t, "abcd", truncateToValidUTF8("abcdef", 4))

	// truncate in middle of multi-byte rune drops the incomplete rune
	// "\u4e2d" is 3 bytes in UTF-8: e4 b8 ad
	s := "abc\u4e2ddef"
	// byte 4 is the start of the rune (e4), byte 5 is continuation (b8)
	result := truncateToValidUTF8(s, 5)
	require.Equal(t, "abc", result) // drops incomplete rune

	// truncate exactly at rune boundary
	result2 := truncateToValidUTF8(s, 6)
	require.Equal(t, "abc\u4e2d", result2)
}
