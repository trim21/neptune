//go:build amd64 && !purego

package rc4

// xorKeyStream is implemented in rc4_amd64.s.
func xorKeyStream(dst, src *byte, n int, state *[256]uint32, i, j *uint8)
