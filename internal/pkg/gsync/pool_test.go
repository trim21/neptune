package gsync_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"tyr/internal/pkg/gsync"
)

func TestPool(t *testing.T) {
	p := gsync.NewPool(func() []byte {
		return make([]byte, 1024)
	})

	b := p.Get()
	require.Equal(t, 1024, len(b))
	require.Equal(t, 1024, cap(b))
}
