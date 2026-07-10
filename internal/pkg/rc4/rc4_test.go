package rc4

import (
	stdrc4 "crypto/rc4"
	"testing"
)

func TestXORKeyStream(t *testing.T) {
	key := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	src := make([]byte, 4096)
	for i := range src {
		src[i] = byte(i)
	}

	// Our implementation
	c1, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	dst1 := make([]byte, 4096)
	c1.XORKeyStream(dst1, src)

	// Standard library
	c2, err := stdrc4.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	dst2 := make([]byte, 4096)
	c2.XORKeyStream(dst2, src)

	for i := range dst1 {
		if dst1[i] != dst2[i] {
			t.Fatalf("mismatch at byte %d: got %02x, want %02x", i, dst1[i], dst2[i])
		}
	}
}

func BenchmarkXORKeyStream(b *testing.B) {
	key := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c, _ := NewCipher(key)
	src := make([]byte, 16384)
	dst := make([]byte, 16384)
	b.SetBytes(16384)
	b.ResetTimer()
	for b.Loop() {
		c.XORKeyStream(dst, src)
	}
}

func BenchmarkStdXORKeyStream(b *testing.B) {
	key := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	c, _ := stdrc4.NewCipher(key)
	src := make([]byte, 16384)
	dst := make([]byte, 16384)
	b.SetBytes(16384)
	b.ResetTimer()
	for b.Loop() {
		c.XORKeyStream(dst, src)
	}
}
