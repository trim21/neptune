//go:build !amd64 || purego

package rc4

import "unsafe"

// Pure Go fallback for platforms without assembly.
func xorKeyStream(dst, src *byte, n int, state *[256]uint32, i, j *uint8) {
	ii, jj := *i, *j
	d := unsafe.Slice(dst, n)
	s := unsafe.Slice(src, n)
	for k := range n {
		ii++
		x := state[ii]
		jj += uint8(x)
		y := state[jj]
		state[ii], state[jj] = y, x
		d[k] = s[k] ^ uint8(state[uint8(x+y)])
	}
	*i, *j = ii, jj
}
