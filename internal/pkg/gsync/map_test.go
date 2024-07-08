package gsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/gsync"
)

func TestMap(t *testing.T) {
	m := gsync.NewMap[int, int]()
	m.Store(1, 0)
	require.Equal(t, 1, m.Size())

	m.Store(1, 2)
	require.Equal(t, 1, m.Size())
	v, ok := m.Load(1)
	require.Equal(t, 2, v)
	require.True(t, ok)

	_, ok = m.LoadOrStore(1, 1)
	require.True(t, ok)
	require.Equal(t, 1, m.Size())

	_, ok = m.LoadOrStore(2, 2)
	require.False(t, ok)
	require.Equal(t, 2, m.Size())

	v, ok = m.LoadAndDelete(1)
	require.True(t, ok)
	require.Equal(t, 1, v)
	require.Equal(t, 1, m.Size())

}
