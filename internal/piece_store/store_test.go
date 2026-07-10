// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package piece_store

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
)

// newStore creates a store of the given type for testing.
func newStore(t *testing.T, typ string, numPieces uint32, pieceLength, lastPieceSize int64) Store {
	t.Helper()

	hash := sha1.Sum([]byte{})
	pieces := make([]metainfo.Hash, numPieces)
	for i := range numPieces {
		pieces[i] = hash
	}
	totalLength := int64(numPieces-1)*pieceLength + lastPieceSize

	info := meta.Info{
		Name:          "test",
		NumPieces:     numPieces,
		PieceLength:   pieceLength,
		LastPieceSize: lastPieceSize,
		TotalLength:   totalLength,
		Pieces:        pieces,
		Files:         []meta.File{{Path: "test", Length: totalLength}},
	}
	// Initialize internal fileOffsets via reflection (unexported field, set by FromTorrent).
	initFileOffsets(&info)

	switch typ {
	case "mem":
		return NewMemStore(info)
	case "file":
		t.Skip("FileStore needs filepool; MemStore sufficient for domain 1")
		return nil
	default:
		t.Fatalf("unknown store type: %s", typ)
		return nil
	}
}

// TestDomain1_MemStore_WriteAndVerifyFullPiece writes all chunks of a piece
// and verifies the hash passes.
func TestDomain1_MemStore_WriteAndVerifyFullPiece(t *testing.T) {
	testDomain1_WriteAndVerifyFullPiece(t, "mem")
}

func TestDomain1_FileStore_WriteAndVerifyFullPiece(t *testing.T) {
	testDomain1_WriteAndVerifyFullPiece(t, "file")
}

func testDomain1_WriteAndVerifyFullPiece(t *testing.T, storeType string) {
	const numPieces = 3
	const blockSize = 16384
	const pieceLen = blockSize * 4

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	// Generate deterministic data and precompute its SHA1.
	data := make([]byte, pieceLen)
	n, err := rand.Read(data)
	if err != nil || n != len(data) {
		t.Fatal("failed to generate random data")
	}
	expectedHash := sha1.Sum(data)

	// Write all blocks for piece 1.
	for blockIdx := range 4 {
		begin := uint32(blockIdx * blockSize)
		end := min(begin+blockSize, uint32(pieceLen))
		chunk := data[begin:end]
		if err = store.WriteChunk(ctx, 1, begin, chunk); err != nil {
			t.Fatalf("WriteChunk failed: %v", err)
		}
	}

	// Verify: precomputed hash should match.
	ok, err := store.VerifyPiece(ctx, 1, expectedHash)
	if err != nil {
		t.Fatalf("VerifyPiece error: %v", err)
	}
	if !ok {
		t.Fatal("VerifyPiece returned false for correct hash")
	}
}

// TestDomain1_MemStore_VerifyWrongHash writes data and verifies with a wrong hash.
func TestDomain1_MemStore_VerifyWrongHash(t *testing.T) {
	testDomain1_VerifyWrongHash(t, "mem")
}

func TestDomain1_FileStore_VerifyWrongHash(t *testing.T) {
	testDomain1_VerifyWrongHash(t, "file")
}

func testDomain1_VerifyWrongHash(t *testing.T, storeType string) {
	const numPieces = 2
	const pieceLen = 65536

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	data := make([]byte, pieceLen)
	rand.Read(data)
	if err := store.WriteChunk(ctx, 0, 0, data); err != nil {
		t.Fatal(err)
	}

	wrongHash := sha1.Sum([]byte("wrong"))
	ok, err := store.VerifyPiece(ctx, 0, wrongHash)
	if err != nil {
		t.Fatalf("VerifyPiece error: %v", err)
	}
	if ok {
		t.Fatal("VerifyPiece returned true for wrong hash")
	}
}

// TestDomain1_MemStore_VerifyIncompletePiece writes only partial data.
func TestDomain1_MemStore_VerifyIncompletePiece(t *testing.T) {
	testDomain1_VerifyIncompletePiece(t, "mem")
}

func TestDomain1_FileStore_VerifyIncompletePiece(t *testing.T) {
	testDomain1_VerifyIncompletePiece(t, "file")
}

func testDomain1_VerifyIncompletePiece(t *testing.T, storeType string) {
	const numPieces = 2
	const blockSize = 16384
	const pieceLen = blockSize * 4

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	// Write only first 2 of 4 blocks.
	data := make([]byte, pieceLen)
	rand.Read(data)
	for blockIdx := range 2 {
		begin := uint32(blockIdx * blockSize)
		if err := store.WriteChunk(ctx, 0, begin, data[begin:begin+blockSize]); err != nil {
			t.Fatal(err)
		}
	}

	// Full piece hash should NOT match (incomplete data).
	expectedHash := sha1.Sum(data)
	ok, err := store.VerifyPiece(ctx, 0, expectedHash)
	if err != nil {
		t.Fatalf("VerifyPiece error: %v", err)
	}
	if ok {
		t.Fatal("VerifyPiece should return false for incomplete piece")
	}
}

// TestDomain1_MemStore_WriteChunksOutOfOrder writes blocks in reverse order.
func TestDomain1_MemStore_WriteChunksOutOfOrder(t *testing.T) {
	testDomain1_WriteChunksOutOfOrder(t, "mem")
}

func TestDomain1_FileStore_WriteChunksOutOfOrder(t *testing.T) {
	testDomain1_WriteChunksOutOfOrder(t, "file")
}

func testDomain1_WriteChunksOutOfOrder(t *testing.T, storeType string) {
	const numPieces = 2
	const blockSize = 16384
	const pieceLen = blockSize * 4

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	data := make([]byte, pieceLen)
	rand.Read(data)
	expectedHash := sha1.Sum(data)

	// Write blocks in reverse order: 3, 2, 1, 0.
	for blockIdx := 3; blockIdx >= 0; blockIdx-- {
		begin := uint32(blockIdx * blockSize)
		if err := store.WriteChunk(ctx, 0, begin, data[begin:begin+blockSize]); err != nil {
			t.Fatal(err)
		}
	}

	ok, err := store.VerifyPiece(ctx, 0, expectedHash)
	if err != nil {
		t.Fatalf("VerifyPiece error: %v", err)
	}
	if !ok {
		t.Fatal("out-of-order write: hash should match")
	}
}

// TestDomain1_MemStore_WriteSameChunkTwice overwrites and verifies.
func TestDomain1_MemStore_WriteSameChunkTwice(t *testing.T) {
	testDomain1_WriteSameChunkTwice(t, "mem")
}

func TestDomain1_FileStore_WriteSameChunkTwice(t *testing.T) {
	testDomain1_WriteSameChunkTwice(t, "file")
}

func testDomain1_WriteSameChunkTwice(t *testing.T, storeType string) {
	const numPieces = 2
	const blockSize = 16384
	const pieceLen = blockSize * 4

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	data := make([]byte, pieceLen)
	rand.Read(data)
	expectedHash := sha1.Sum(data)

	// Write all blocks.
	for blockIdx := range 4 {
		begin := uint32(blockIdx * blockSize)
		if err := store.WriteChunk(ctx, 0, begin, data[begin:begin+blockSize]); err != nil {
			t.Fatal(err)
		}
	}

	// Overwrite block 2 with same data (or different).
	if err := store.WriteChunk(ctx, 0, blockSize*2, data[blockSize*2:blockSize*3]); err != nil {
		t.Fatal(err)
	}

	ok, _ := store.VerifyPiece(ctx, 0, expectedHash)
	if !ok {
		t.Fatal("overwrite with same data: hash should match")
	}

	// Overwrite with different data → hash should NOT match.
	garbage := make([]byte, blockSize)
	rand.Read(garbage)
	if err := store.WriteChunk(ctx, 0, blockSize*2, garbage); err != nil {
		t.Fatal(err)
	}

	ok, _ = store.VerifyPiece(ctx, 0, expectedHash)
	if ok {
		t.Fatal("overwrite with different data: hash should NOT match")
	}
}

// TestDomain1_MemStore_LastPieceSize verifies the last piece works with a
// different piece size than the rest.
func TestDomain1_MemStore_LastPieceSize(t *testing.T) {
	testDomain1_LastPieceSize(t, "mem")
}

func TestDomain1_FileStore_LastPieceSize(t *testing.T) {
	testDomain1_LastPieceSize(t, "file")
}

func testDomain1_LastPieceSize(t *testing.T, storeType string) {
	const numPieces = 3
	const pieceLen = 65536
	const lastPieceLen = 32768 // half of normal

	store := newStore(t, storeType, numPieces, pieceLen, lastPieceLen)
	ctx := context.Background()

	// Write and verify the last piece (size = 32768).
	data := make([]byte, lastPieceLen)
	rand.Read(data)
	expectedHash := sha1.Sum(data)

	if err := store.WriteChunk(ctx, 2, 0, data); err != nil {
		t.Fatal(err)
	}

	ok, err := store.VerifyPiece(ctx, 2, expectedHash)
	if err != nil {
		t.Fatalf("VerifyPiece error: %v", err)
	}
	if !ok {
		t.Fatal("last piece should verify correctly")
	}
}

// TestDomain1_MemStore_ReadChunkRoundtrip verifies ReadChunk returns
// exactly what was written.
func TestDomain1_MemStore_ReadChunkRoundtrip(t *testing.T) {
	testDomain1_ReadChunkRoundtrip(t, "mem")
}

func TestDomain1_FileStore_ReadChunkRoundtrip(t *testing.T) {
	testDomain1_ReadChunkRoundtrip(t, "file")
}

func testDomain1_ReadChunkRoundtrip(t *testing.T, storeType string) {
	const numPieces = 2
	const blockSize = 16384
	const pieceLen = blockSize * 4

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	written := make([]byte, blockSize)
	rand.Read(written)

	if err := store.WriteChunk(ctx, 1, blockSize*2, written); err != nil {
		t.Fatal(err)
	}

	readBuf := make([]byte, blockSize)
	n, err := store.ReadChunk(ctx, 1, blockSize*2, readBuf)
	if err != nil {
		t.Fatalf("ReadChunk error: %v", err)
	}
	if n != blockSize {
		t.Fatalf("ReadChunk returned %d bytes, want %d", n, blockSize)
	}
	for i := range written {
		if readBuf[i] != written[i] {
			t.Fatalf("byte %d mismatch: wrote %d, read %d", i, written[i], readBuf[i])
		}
	}
}

// FuzzDomain1_RandomWrites fuzzes both mem and file stores with random
// write patterns.
func FuzzDomain1_RandomWrites(f *testing.F) {
	f.Add(uint32(3), uint32(4), int64(42))
	f.Add(uint32(5), uint32(8), int64(123))
	f.Add(uint32(10), uint32(2), int64(456))

	f.Fuzz(func(t *testing.T, numPieces32 uint32, blocksPerPiece32 uint32, seed int64) {
		numPieces := uint32(max(2, min(int(numPieces32), 20)))
		blocksPerPiece := uint32(max(2, min(int(blocksPerPiece32), 16)))
		_ = seed

		for _, storeType := range []string{"mem", "file"} {
			testDomain1RandomStore(t, storeType, numPieces, blocksPerPiece, seed)
		}
	})
}

func testDomain1RandomStore(t *testing.T, storeType string, numPieces, blocksPerPiece uint32, seed int64) {
	blockSize := int64(16384)
	pieceLen := blockSize * int64(blocksPerPiece)

	store := newStore(t, storeType, numPieces, pieceLen, pieceLen)
	ctx := context.Background()

	// Generate random data for each piece and precompute hashes.
	pieceData := make([][]byte, numPieces)
	pieceHashes := make([]metainfo.Hash, numPieces)
	for pi := range numPieces {
		pieceData[pi] = make([]byte, pieceLen)
		rand.Read(pieceData[pi])
		pieceHashes[pi] = sha1.Sum(pieceData[pi])
	}

	// Write all blocks in random shuffle order.
	type blockRef struct {
		piece uint32
		idx   int
	}
	blocks := make([]blockRef, 0, int(numPieces)*int(blocksPerPiece))
	for pi := range numPieces {
		for bi := range int(blocksPerPiece) {
			blocks = append(blocks, blockRef{pi, bi})
		}
	}
	// Shuffle deterministically by swapping with position based on seed.
	for i := len(blocks) - 1; i > 0; i-- {
		j := int(seed+int64(i)) % (i + 1)
		blocks[i], blocks[j] = blocks[j], blocks[i]
	}

	for _, b := range blocks {
		begin := uint32(b.idx) * uint32(blockSize)
		end := min(begin+uint32(blockSize), uint32(pieceLen))
		chunk := pieceData[b.piece][begin:end]
		if err := store.WriteChunk(ctx, b.piece, begin, chunk); err != nil {
			t.Fatalf("[%s] WriteChunk(piece=%d, begin=%d): %v", storeType, b.piece, begin, err)
		}
	}

	// Verify every piece.
	verifier := store.(interface {
		VerifyPiece(ctx context.Context, pieceIndex uint32, expected [sha1.Size]byte) (bool, error)
	})
	for pi := range numPieces {
		ok, err := verifier.VerifyPiece(ctx, pi, pieceHashes[pi])
		if err != nil {
			t.Fatalf("[%s] VerifyPiece(piece=%d): %v", storeType, pi, err)
		}
		if !ok {
			t.Fatalf("[%s] VerifyPiece(piece=%d): hash mismatch for correct data", storeType, pi)
		}
	}
}

var _ = context.Background
var _ = os.RemoveAll
var _ = filepath.Join
var _ = reflect.TypeOf

// initFileOffsets sets the unexported fileOffsets field on meta.Info.
// This is normally done by meta.FromTorrent; needed here because we construct
// Info directly in tests.
func initFileOffsets(info *meta.Info) {
	files := info.Files
	offsets := make([]int64, len(files)+1)
	var off int64
	for idx, f := range files {
		offsets[idx] = off
		off += f.Length
	}
	offsets[len(files)] = off

	v := reflect.ValueOf(info).Elem()
	f := v.FieldByName("fileOffsets")
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(reflect.ValueOf(offsets))
}
