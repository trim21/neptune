//go:build !release

package as_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/as"
)

func TestUint32(t *testing.T) {
	require.Equal(t, uint32(5), as.Uint32(int8(5)))
	require.Equal(t, uint32(5), as.Uint32(int16(5)))
	require.Equal(t, uint32(5), as.Uint32(int32(5)))
	require.Equal(t, uint32(5), as.Uint32(int64(5)))
	require.Equal(t, uint32(5), as.Uint32(int(5)))
	require.Equal(t, uint32(5), as.Uint32(uint8(5)))
	require.Equal(t, uint32(5), as.Uint32(uint16(5)))
	require.Equal(t, uint32(5), as.Uint32(uint64(5)))
	require.Equal(t, uint32(5), as.Uint32(uint(5)))

	require.Panics(t, func() {
		as.Uint32(math.MaxUint32 + 1)
	})
}

func TestUint64(t *testing.T) {
	require.Equal(t, uint64(5), as.Uint64(int8(5)))
	require.Equal(t, uint64(5), as.Uint64(int16(5)))
	require.Equal(t, uint64(5), as.Uint64(int32(5)))
	require.Equal(t, uint64(5), as.Uint64(int64(5)))
	require.Equal(t, uint64(5), as.Uint64(int(5)))
	require.Equal(t, uint64(5), as.Uint64(uint8(5)))
	require.Equal(t, uint64(5), as.Uint64(uint16(5)))
	require.Equal(t, uint64(5), as.Uint64(uint32(5)))
	require.Equal(t, uint64(5), as.Uint64(uint(5)))

	require.Panics(t, func() {
		as.Uint64(-1)
	})
}
