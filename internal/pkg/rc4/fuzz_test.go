package rc4

import (
	stdrc4 "crypto/rc4"
	"testing"
)

func FuzzXORKeyStream(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, make([]byte, 256))
	f.Add(make([]byte, 1), make([]byte, 0))
	f.Add([]byte{0xff}, make([]byte, 1))
	f.Add(make([]byte, 256), make([]byte, 4096))
	f.Add([]byte{0x00, 0x00, 0x00, 0x00, 0x00}, make([]byte, 16384))

	f.Fuzz(func(t *testing.T, key, src []byte) {
		if len(key) < 1 || len(key) > 256 {
			t.Skip()
		}

		// Our implementation
		c1, err := NewCipher(key)
		if err != nil {
			t.Fatalf("NewCipher: %v", err)
		}
		dst1 := make([]byte, len(src))
		c1.XORKeyStream(dst1, src)

		// Standard library
		c2, err := stdrc4.NewCipher(key)
		if err != nil {
			t.Fatalf("stdlib NewCipher: %v", err)
		}
		dst2 := make([]byte, len(src))
		c2.XORKeyStream(dst2, src)

		for i := range dst1 {
			if dst1[i] != dst2[i] {
				t.Fatalf("mismatch at byte %d/%d: our=0x%02x std=0x%02x\nkey=%x\nsrc[%d]=0x%02x", i, len(src), dst1[i], dst2[i], key, i, src[i])
			}
		}

		// Verify repeated XORKeyStream calls maintain correct state
		// (state is updated across calls)
		for round := 0; round < 3 && len(src) > 0; round++ {
			offset := round % len(src)
			chunk := src[offset:]
			d1 := make([]byte, len(chunk))
			d2 := make([]byte, len(chunk))
			c1.XORKeyStream(d1, chunk)
			c2.XORKeyStream(d2, chunk)
			for i := range d1 {
				if d1[i] != d2[i] {
					t.Fatalf("round %d mismatch at byte %d/%d", round+1, i, len(chunk))
				}
			}
		}
	})
}
