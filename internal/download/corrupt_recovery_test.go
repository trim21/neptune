// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"crypto/sha1"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"golang.org/x/sync/semaphore"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/proto"
	"neptune/internal/session"
)

// testEnv is a minimal test environment for download testing.
type testEnv struct {
	t   *testing.T
	d   *Download
	env *piece_store.MemStore
}

// newTestEnv creates a download with numPieces pieces, each blocksPerPiece blocks wide.
// The store is wrapped with failStore (if not nil) to simulate hash failures.
func newTestEnv(t *testing.T, numPieces, blocksPerPiece uint32, failPieces []uint32) *testEnv {
	t.Helper()

	pieceLength := int64(blocksPerPiece) * defaultBlockSize
	totalLength := int64(numPieces) * pieceLength

	// All pieces use zero-filled data with matching hashes.
	zeroPiece := make([]byte, pieceLength)
	hash := sha1.Sum(zeroPiece)
	pieces := make([]metainfo.Hash, numPieces)
	for i := range numPieces {
		pieces[i] = hash
	}

	info := meta.Info{
		Name:          "test",
		NumPieces:     numPieces,
		PieceLength:   pieceLength,
		LastPieceSize: pieceLength,
		TotalLength:   totalLength,
		Pieces:        pieces,
		Files:         []meta.File{{Path: "test", Length: totalLength}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	completedBm := bm.New(info.NumPieces)
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	missingBm.Fill()
	normalChunkLen := info.BlocksPerPiece()
	stateCond := gsync.NewCond(&sync.RWMutex{})

	memStore := piece_store.NewMemStore(info)
	var store = memStore
	if len(failPieces) > 0 {
		store = NewFailNPieceStore(memStore, failPieces)
	}

	d := &Download{
		ctx:    ctx,
		info:   info,
		cancel: cancel,
		log:    zerolog.New(zerolog.Nop()),
		store:  store,
		session: &session.Session{
			ConnSem: semaphore.NewWeighted(200),
		},
		normalChunkLen:    normalChunkLen,
		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		ioDownloadRate:    flowrate.New(time.Second, 5*time.Second),
		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),
		uploadLimiter:     ratelimit.New(0),
		downloadLimiter:   ratelimit.New(0),
		peers:             xsync.NewMap[uint64, Peer](),
		connectedAddrs:    xsync.NewMap[netip.AddrPort, Peer](),
		stateCond:         stateCond,
		private:           false,
		corruptedPieces:   make(map[uint32]int),
		tracker:           tracker.New(ctx, tracker.Config{}),
	}
	d.session.DownloadLimiter = ratelimit.New(0)
	d.session.UploadLimiter = ratelimit.New(0)
	d.session.PieceDownloadRate = flowrate.New(time.Second, 5*time.Second)

	d.completedBm = completedBm
	d.missingBm = missingBm
	d.wantedBm = wantedBm
	d.peerList = newPeerList(d)
	d.picker.Store(NewPiecePicker(info, missingBm, nil, nil))
	d.state.Store(uint32(Downloading))

	return &testEnv{t: t, d: d, env: memStore.(*piece_store.MemStore)}
}

// sendPiece sends all blocks of a piece (zero-filled data).
func (env *testEnv) sendPiece(pieceIndex uint32) {
	env.t.Helper()
	d := env.d
	var h heap.Heap[responseChunk]
	doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}
	data := make([]byte, d.info.PieceLength)
	for bi := range d.info.PieceBlockCount(pieceIndex) {
		ci := pieceChunk(d.info, pieceIndex, bi)
		handleRes(d, &h, pc, doneBm, pendingBm, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
			PieceIndex: pieceIndex, Begin: ci.Begin,
			Data: data[ci.Begin : ci.Begin+ci.Length],
		}})
	}
}

// sendAllPieces sends all pieces.
func (env *testEnv) sendAllPieces() {
	env.t.Helper()
	for pi := range env.d.info.NumPieces {
		env.sendPiece(pi)
	}
}

// waitHashCheck waits for async hash checks to complete.
func (env *testEnv) waitHashCheck() {
	time.Sleep(200 * time.Millisecond)
}

// assertCompleted verifies all pieces are completed.
func (env *testEnv) assertCompleted() {
	env.t.Helper()
	for pi := range env.d.info.NumPieces {
		if !env.d.completedBm.Contains(pi) {
			env.t.Fatalf("piece %d should be completed", pi)
		}
	}
}

// assertNotCompleted verifies the given pieces are NOT completed.
func (env *testEnv) assertNotCompleted(pieces ...uint32) {
	env.t.Helper()
	for _, pi := range pieces {
		if env.d.completedBm.Contains(pi) {
			env.t.Fatalf("piece %d should not be completed", pi)
		}
	}
}

// pickerHasPiece checks if the piece is in the picker's candidate list.
func (env *testEnv) pickerHasPiece(pieceIndex uint32) bool {
	return env.d.picker.Load().IsPieceInCandidates(pieceIndex)
}

// TestCorruptPieceRecovery tests that when a piece fails hash check on first
// attempt, it can be re-downloaded and complete successfully.
func TestCorruptPieceRecovery(t *testing.T) {
	const numPieces = 4
	const blocksPerPiece = 4

	// Pieces 0 and 2 will fail hash check on first attempt.
	env := newTestEnv(t, numPieces, blocksPerPiece, []uint32{0, 2})

	// Round 1: send all pieces (0 and 2 will fail hash).
	env.sendAllPieces()
	env.waitHashCheck()

	env.assertNotCompleted(0, 2)

	// Corrupt pieces should be back in picker candidates.
	if !env.pickerHasPiece(0) {
		t.Fatal("piece 0 should be in picker candidates after hash failure")
	}
	if !env.pickerHasPiece(2) {
		t.Fatal("piece 2 should be in picker candidates after hash failure")
	}

	// Round 2: re-send the failed pieces (now hash will pass).
	env.sendPiece(0)
	env.sendPiece(2)
	env.waitHashCheck()

	env.assertCompleted()
}

// TestCorruptPieceRecoverySingleBlock tests recovery for single-block pieces.
func TestCorruptPieceRecoverySingleBlock(t *testing.T) {
	env := newTestEnv(t, 8, 1, []uint32{3, 5, 7})

	env.sendAllPieces()
	env.waitHashCheck()

	env.assertNotCompleted(3, 5, 7)

	env.sendPiece(3)
	env.sendPiece(5)
	env.sendPiece(7)
	env.waitHashCheck()

	env.assertCompleted()
}

// TestAllPiecesCorrupt tests that all pieces can fail and be recovered.
func TestAllPiecesCorrupt(t *testing.T) {
	env := newTestEnv(t, 3, 8, []uint32{0, 1, 2})

	env.sendAllPieces()
	env.waitHashCheck()

	env.assertNotCompleted(0, 1, 2)

	env.sendPiece(0)
	env.sendPiece(1)
	env.sendPiece(2)
	env.waitHashCheck()

	env.assertCompleted()
}

// TestNoCorruption tests that download completes normally without any failures.
func TestNoCorruption(t *testing.T) {
	env := newTestEnv(t, 4, 4, nil)

	env.sendAllPieces()
	env.waitHashCheck()

	env.assertCompleted()
}
