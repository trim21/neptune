// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
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

func newTestDownload(t testing.TB, numPieces uint32, blocksPerPiece uint32, newStore func(info meta.Info) piece_store.PieceStore) *Download {
	t.Helper()

	pieceLength := int64(blocksPerPiece) * defaultBlockSize
	totalLength := int64(numPieces) * pieceLength

	// Precompute SHA1 of zero-filled piece data so checkPiece passes.
	zeroPiece := make([]byte, pieceLength)
	pieces := make([]metainfo.Hash, numPieces)
	for i := range numPieces {
		pieces[i] = sha1.Sum(zeroPiece)
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
	normalChunkLen := info.BlocksPerPiece()
	stateCond := gsync.NewCond(&sync.RWMutex{})

	d := &Download{
		ctx:          ctx,
		bitfieldSize: (info.NumPieces + 7) / 8,
		session: &session.Session{
			ConnSem:           semaphore.NewWeighted(200),
			DownloadLimiter:   ratelimit.New(0),
			UploadLimiter:     ratelimit.New(0),
			PieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		},
		cancel:         cancel,
		log:            zerolog.New(zerolog.Nop()),
		info:           info,
		store:          newStore(info),
		normalChunkLen: normalChunkLen,

		pieceDownloadRate:      flowrate.New(time.Second, 5*time.Second),
		ioDownloadRate:         flowrate.New(time.Second, 5*time.Second),
		pieceUploadRate:        flowrate.New(time.Second, 5*time.Second),
		uploadLimiter:          ratelimit.New(0),
		downloadLimiter:        ratelimit.New(0),
		peers:                  xsync.NewMap[uint64, Peer](),
		connectedAddrs:         xsync.NewMap[netip.AddrPort, Peer](),
		stateCond:              stateCond,
		private:                false,
		corruptedPieces:        make(map[uint32]int),
		bannedAddrs:            make(map[netip.Addr]time.Time),
		tracker:                tracker.New(ctx, tracker.Config{}),
		scheduleResponseSignal: make(chan empty.Empty, 1),
		pendingPeersSignal:     make(chan empty.Empty, 1),
	}
	wantedBm := bm.New(info.NumPieces)
	wantedBm.Fill()
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	missingBm.Fill()
	d.completedBm = completedBm
	d.missingBm = missingBm
	d.wantedBm = wantedBm
	d.peerList = newPeerList(d)
	d.picker.Store(NewPiecePicker(info, missingBm, nil, nil, NewRequestGate(&d.state, uint32(Downloading))))
	d.state.Store(uint32(Downloading))
	return d
}

func resetDownload(d *Download, done, pending *bm.NilSafeLockFreeBitmap) {
	d.completedBm.Clear()
	d.missingBm.Fill()
	d.picker.Load().ResetAll()
	d.completed.Store(0)
	d.downloaded.Store(0)
	pending.Clear()
	done.Clear()
	d.state.Store(uint32(Downloading))
	d.corruptedPiecesMu.Lock()
	d.corruptedPieces = make(map[uint32]int)
	d.corruptedPiecesMu.Unlock()
}

type chunkDesc struct {
	pieceIndex uint32
	begin      uint32
	length     uint32
	pi         uint32
}

func dumpState(d *Download, h *heap.Heap[responseChunk], doneBm, pendingBm *bm.NilSafeLockFreeBitmap) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n=== State ===\n")
	fmt.Fprintf(&sb, "pieces=%d chunkLen=%d", d.info.NumPieces, d.normalChunkLen)
	if h != nil {
		fmt.Fprintf(&sb, " heap=%d", h.Len())
	}
	sb.WriteByte('\n')

	pendingList := make([]uint32, 0)
	doneList := make([]uint32, 0)
	totalChunks := d.info.NumPieces * d.normalChunkLen
	for i := range totalChunks {
		if pendingBm.Contains(i) {
			pendingList = append(pendingList, i)
		}
		if doneBm.Contains(i) {
			doneList = append(doneList, i)
		}
	}
	fmt.Fprintf(&sb, "pending=%v\n", pendingList)
	fmt.Fprintf(&sb, "done=%v\n", doneList)
	for pi := range d.info.NumPieces {
		total := d.info.PieceBlockCount(pi)
		start := pi * d.normalChunkLen
		end := start + uint32(total)
		p := 0
		dd := 0
		for i := start; i < end; i++ {
			if pendingBm.Contains(i) {
				p++
			}
			if doneBm.Contains(i) {
				dd++
			}
		}
		fmt.Fprintf(&sb, "  piece %d: total=%d pending=%d done=%d\n", pi, total, p, dd)
	}
	return sb.String()
}

func assertHandleResCompleted(
	t *testing.T,
	d *Download,
	h *heap.Heap[responseChunk],
	doneBm, pendingBm *bm.NilSafeLockFreeBitmap,
	label string,
) {
	t.Helper()
	if h.Len() != 0 {
		t.Errorf("FAIL %s: heap=%d\n%s", label, h.Len(), dumpState(d, h, doneBm, pendingBm))
		return
	}

	if !assert.Eventually(t, func() bool {
		return d.completedBm.Count() == d.info.NumPieces && d.GetState() == Seeding
	}, 2*time.Second, time.Millisecond, "download did not complete for %s", label) {
		t.Log(dumpState(d, h, doneBm, pendingBm))
	}
}

// TestHandleResOrder sends all chunks in various orders through handleRes
// using an in-memory store for writes. The heap is drained synchronously;
// piece verification and the final transition to Seeding are asynchronous.
func TestHandleResOrder(t *testing.T) {
	const numPieces = 5
	const blocksPerPiece = 4

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

	var all []chunkDesc
	for pi := range uint32(numPieces) {
		nb := (d.info.PieceBlockCount(pi))
		for bi := range nb {
			ch := pieceChunk(d.info, pi, bi)
			all = append(all, chunkDesc{
				pieceIndex: pi, begin: ch.Begin, length: ch.Length,
				pi: pi*d.normalChunkLen + uint32(bi),
			})
		}
	}

	orders := map[string][]chunkDesc{"sequential": all}

	rev := make([]chunkDesc, len(all))
	copy(rev, all)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	orders["reverse"] = rev

	{
		var o []chunkDesc
		for pi := range uint32(numPieces) {
			var piece []chunkDesc
			for _, c := range all {
				if c.pieceIndex == pi {
					piece = append(piece, c)
				}
			}
			for _, v := range slices.Backward(piece) {
				o = append(o, v)
			}
		}
		orders["intra_piece_reverse"] = o
	}

	{
		var o []chunkDesc
		for pi := range uint32(numPieces) {
			for _, c := range all {
				if c.pieceIndex == pi && int(c.begin/defaultBlockSize) == (d.info.PieceBlockCount(pi))-1 {
					o = append(o, c)
				}
			}
		}
		for _, c := range all {
			found := false
			for _, x := range o {
				if x.pi == c.pi {
					found = true
					break
				}
			}
			if !found {
				o = append(o, c)
			}
		}
		orders["last_block_first"] = o
	}

	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			// Fresh download per subtest to avoid async checkPiece
			// goroutines leaking from one test to another.
			d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
			var h heap.Heap[responseChunk]
			doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
			pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
			pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}
			for _, c := range order {
				handleRes(d, &h, pc, doneBm, pendingBm, claimedSubmitForTest(t, d, chunkSubmit{
					res: &proto.ChunkResponse{
						PieceIndex: c.pieceIndex, Begin: c.begin,
						Data: make([]byte, c.length),
					},
					peerID: 0,
				}))
			}
			assertHandleResCompleted(t, d, &h, doneBm, pendingBm, name)
		})
	}
}

func TestHandleResLargePiece(t *testing.T) {
	const numPieces = 3
	const blocksPerPiece = 256

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

	var h heap.Heap[responseChunk]
	doneBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pendingBm := bm.NewNilSafeLockFreeBitmap(d.info.TotalBlockCount())
	pc := &peerContributors{m: make(map[uint32]map[uint64]empty.Empty)}

	var all []chunkDesc
	for pi := range uint32(numPieces) {
		nb := d.info.PieceBlockCount(pi)
		for bi := range nb {
			ch := pieceChunk(d.info, pi, bi)
			all = append(all, chunkDesc{
				pieceIndex: pi, begin: ch.Begin, length: ch.Length,
				pi: pi*d.normalChunkLen + uint32(bi),
			})
		}
	}

	// Round-robin across pieces.
	var order []chunkDesc
	byPiece := make([][]chunkDesc, numPieces)
	for _, c := range all {
		byPiece[c.pieceIndex] = append(byPiece[c.pieceIndex], c)
	}
	idx := make([]int, numPieces)
	for {
		added := false
		for pi := range uint32(numPieces) {
			if idx[pi] < len(byPiece[pi]) {
				order = append(order, byPiece[pi][idx[pi]])
				idx[pi]++
				added = true
			}
		}
		if !added {
			break
		}
	}

	for _, c := range order {
		handleRes(d, &h, pc, doneBm, pendingBm, claimedSubmitForTest(t, d, chunkSubmit{peerID: 0, res: &proto.ChunkResponse{
			PieceIndex: c.pieceIndex, Begin: c.begin,
			Data: make([]byte, c.length),
		}}))
	}

	assertHandleResCompleted(t, d, &h, doneBm, pendingBm, "large")
}

// FuzzHandleRes uses Go's fuzzing engine to randomize chunk arrival order
// and verify the synchronous state machine invariants hold for any order.
func FuzzHandleRes(f *testing.F) {
	f.Add(int64(42))
	f.Add(int64(12345))
	f.Add(int64(1))

	const numPieces = 5
	const blocksPerPiece = 4

	f.Fuzz(func(t *testing.T, seed int64) {
		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
		d.state.Store(uint32(Downloading))

		var all []chunkDesc
		for pi := range uint32(numPieces) {
			nb := d.info.PieceBlockCount(pi)
			for bi := range nb {
				ch := pieceChunk(d.info, pi, bi)
				all = append(all, chunkDesc{
					pieceIndex: pi, begin: ch.Begin, length: ch.Length,
					pi: pi*d.normalChunkLen + uint32(bi),
				})
			}
		}

		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)>>32))
		rng.Shuffle(len(all), func(i, j int) {
			all[i], all[j] = all[j], all[i]
		})

		d.resChan = make(chan chunkSubmit, len(all))
		go d.backgroundResHandler()

		for _, c := range all {
			d.resChan <- claimedSubmitForTest(t, d, chunkSubmit{
				res: &proto.ChunkResponse{
					PieceIndex: c.pieceIndex, Begin: c.begin,
					Data: make([]byte, c.length),
				},
				peerID: 0,
			})
		}

		waitDownloadDone(t, d, numPieces, seed)
	})
}

// FuzzHandleResDuplicates sends 2-4 copies of every chunk in random order,
// stressing endgame duplicate handling.
func FuzzHandleResDuplicates(f *testing.F) {
	f.Add(int64(42))
	f.Add(int64(12345))

	const numPieces = 5
	const blocksPerPiece = 4

	f.Fuzz(func(t *testing.T, seed int64) {
		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)
		d.state.Store(uint32(Downloading))

		totalChunks := 0
		for pi := range uint32(numPieces) {
			nb := d.info.PieceBlockCount(pi)
			totalChunks += nb
		}

		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)>>32))
		copies := int(2 + uint64(rng.IntN(3)))
		all := make([]chunkDesc, 0, totalChunks*copies)

		for range copies {
			for pi := range uint32(numPieces) {
				nb := d.info.PieceBlockCount(pi)
				for bi := range nb {
					ch := pieceChunk(d.info, pi, bi)
					all = append(all, chunkDesc{
						pieceIndex: pi, begin: ch.Begin, length: ch.Length,
						pi: pi*d.normalChunkLen + uint32(bi),
					})
				}
			}
		}

		rng.Shuffle(len(all), func(i, j int) {
			all[i], all[j] = all[j], all[i]
		})

		d.resChan = make(chan chunkSubmit, len(all))
		go d.backgroundResHandler()

		claims := make(map[uint32]BlockClaim, totalChunks)
		for _, c := range all {
			claim, ok := claims[c.pi]
			if !ok {
				claim = claimBlockForTest(t, d, c.pieceIndex, c.begin/defaultBlockSize, 0)
				claims[c.pi] = claim
			}
			d.resChan <- chunkSubmit{
				res: &proto.ChunkResponse{
					PieceIndex: c.pieceIndex, Begin: c.begin,
					Data: make([]byte, c.length),
				},
				peerID: 0,
				claim:  claim,
			}
		}

		waitDownloadDone(t, d, numPieces, seed)
	})
}

func waitDownloadDone(t *testing.T, d *Download, numPieces uint32, seed int64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			d.cancel()
			time.Sleep(10 * time.Millisecond)
			t.Errorf("seed=%d timeout\n%s", seed, dumpState(d, nil, nil, nil))
			return
		case <-ticker.C:
			if d.completedBm.Count() == numPieces {
				d.cancel()
				time.Sleep(10 * time.Millisecond)
				return
			}
		}
	}
}
