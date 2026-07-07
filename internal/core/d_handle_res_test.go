//go:build !release

package core

import (
	"context"
	"crypto/sha1"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"

	"neptune/internal/core/tracker"
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
	normalChunkLen := (info.PieceLength + defaultBlockSize - 1) / defaultBlockSize
	stateCond := gsync.NewCond(&sync.RWMutex{})

	d := &Download{
		ctx:    ctx,
		cancel: cancel,
		info:   info,
		c: &Client{
			downloadLimiter:   ratelimit.New(0),
			uploadLimiter:     ratelimit.New(0),
			pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		},
		log:            zerolog.New(zerolog.Nop()),
		store:          newStore(info),
		normalChunkLen: uint32(normalChunkLen),
		completedBm:    completedBm,
		picker:         newPiecePicker(info, completedBm),
		chunk: chunkState{
			done: make(bitmap.Bitmap, (int64(info.NumPieces)*(normalChunkLen)+63)/64),
			mu:   sync.RWMutex{},
		},
		pieceInfo:             piece_store.BuildPieceInfos(info),
		pieceDownloadRate:     flowrate.New(time.Second, 5*time.Second),
		ioDownloadRate:        flowrate.New(time.Second, 5*time.Second),
		pieceUploadRate:       flowrate.New(time.Second, 5*time.Second),
		uploadLimiter:         ratelimit.New(0),
		downloadLimiter:       ratelimit.New(0),
		peers:                 xsync.NewMap[uint64, *Peer](),
		stateCond:             stateCond,
		private:               false,
		corruptedPieces:       make(map[uint32]int),
		scheduleRequestSignal: make(chan empty.Empty, 1),
		Trk:                   tracker.New(ctx, tracker.Config{}),
	}
	d.state.Store(uint32(Downloading))
	return d
}

func resetDownload(d *Download) {
	d.completedBm.Clear()
	d.picker.resetAll()
	d.completed.Store(0)
	d.downloaded.Store(0)
	d.chunk.heap = heap.Heap[responseChunk]{}
	d.chunk.pending = nil
	for i := range d.chunk.done {
		d.chunk.done[i] = 0
	}
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

func dumpState(d *Download) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n=== State ===\n")
	fmt.Fprintf(&sb, "pieces=%d chunkLen=%d heap=%d\n",
		d.info.NumPieces, d.normalChunkLen, d.chunk.heap.Len())

	d.chunk.mu.RLock()
	pending := make([]uint32, 0)
	done := make([]uint32, 0)
	totalChunks := d.info.NumPieces * d.normalChunkLen
	for i := range totalChunks {
		if d.chunk.pending.Contains(i) {
			pending = append(pending, i)
		}
		if d.chunk.done.Contains(i) {
			done = append(done, i)
		}
	}
	d.chunk.mu.RUnlock()
	fmt.Fprintf(&sb, "pending=%v\n", pending)
	fmt.Fprintf(&sb, "done=%v\n", done)
	for pi := range d.info.NumPieces {
		total := int(piece_store.PieceChunksCount(d.info, pi))
		start := pi * d.normalChunkLen
		end := start + uint32(total)
		p := 0
		dd := 0
		d.chunk.mu.RLock()
		for i := start; i < end; i++ {
			if d.chunk.pending.Contains(i) {
				p++
			}
			if d.chunk.done.Contains(i) {
				dd++
			}
		}
		d.chunk.mu.RUnlock()
		fmt.Fprintf(&sb, "  piece %d: total=%d pending=%d done=%d\n", pi, total, p, dd)
	}
	return sb.String()
}

func allDone(d *Download) bool {
	for pi := range d.info.NumPieces {
		total := int(piece_store.PieceChunksCount(d.info, pi))
		done := 0
		start := pi * d.normalChunkLen
		end := start + uint32(total)
		d.chunk.mu.RLock()
		for i := start; i < end; i++ {
			if d.chunk.done.Contains(i) {
				done++
			}
		}
		d.chunk.mu.RUnlock()
		if done != total {
			return false
		}
	}
	return true
}

// TestHandleResOrder sends all chunks in various orders through handleRes
// using an in-memory store for writes. Async checkPiece runs against
// empty temp files and fails, which is fine — we verify the synchronous
// state machine invariants: heap must be empty and all done bits set
// after all chunks are processed.
func TestHandleResOrder(t *testing.T) {
	const numPieces = 5
	const blocksPerPiece = 4

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

	var all []chunkDesc
	for pi := range uint32(numPieces) {
		nb := int(piece_store.PieceChunksCount(d.info, pi))
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
				if c.pieceIndex == pi && int(c.begin/defaultBlockSize) == int(piece_store.PieceChunksCount(d.info, pi))-1 {
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
			for _, c := range order {
				d.handleRes(&proto.ChunkResponse{
					PieceIndex: c.pieceIndex, Begin: c.begin,
					Data: make([]byte, c.length),
				})
			}
			if h := d.chunk.heap.Len(); h != 0 || !allDone(d) {
				t.Errorf("FAIL %s: heap=%d\n%s", name, h, dumpState(d))
			} else {
				t.Logf("PASS %s", name)
			}
		})
	}
}

func TestHandleResLargePiece(t *testing.T) {
	const numPieces = 3
	const blocksPerPiece = 256

	d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

	var all []chunkDesc
	for pi := range uint32(numPieces) {
		nb := int(piece_store.PieceChunksCount(d.info, pi))
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
		d.handleRes(&proto.ChunkResponse{
			PieceIndex: c.pieceIndex, Begin: c.begin,
			Data: make([]byte, c.length),
		})
	}

	if h := d.chunk.heap.Len(); h != 0 || !allDone(d) {
		t.Errorf("FAIL large: heap=%d\n%s", h, dumpState(d))
	} else {
		t.Logf("PASS large (%d chunks)", len(order))
	}
}

// FuzzHandleRes uses Go's fuzzing engine to randomize chunk arrival order
// and verify the synchronous state machine invariants hold for any order.
func FuzzHandleRes(f *testing.F) {
	// Seed corpus with a few known-good seeds.
	f.Add(int64(42))
	f.Add(int64(12345))
	f.Add(int64(1))

	const numPieces = 5
	const blocksPerPiece = 4

	f.Fuzz(func(t *testing.T, seed int64) {
		d := newTestDownload(t, numPieces, blocksPerPiece, piece_store.NewMemStore)

		var all []chunkDesc
		for pi := range uint32(numPieces) {
			nb := int(piece_store.PieceChunksCount(d.info, pi))
			for bi := range nb {
				ch := pieceChunk(d.info, pi, bi)
				all = append(all, chunkDesc{
					pieceIndex: pi, begin: ch.Begin, length: ch.Length,
					pi: pi*d.normalChunkLen + uint32(bi),
				})
			}
		}

		// Fisher-Yates shuffle with deterministic seed.
		rng := &seededRand{seed: uint64(seed)}
		for i := len(all) - 1; i > 0; i-- {
			j := rng.next() % uint64(i+1)
			all[i], all[j] = all[j], all[i]
		}

		for _, c := range all {
			d.handleRes(&proto.ChunkResponse{
				PieceIndex: c.pieceIndex, Begin: c.begin,
				Data: make([]byte, c.length),
			})
		}

		// Cancel context to signal async checkPiece goroutines to stop, then
		// wait briefly for them to drain before checking invariants.
		d.cancel()
		time.Sleep(10 * time.Millisecond)

		if h := d.chunk.heap.Len(); h != 0 || !allDone(d) {
			t.Errorf("seed=%d heap=%d\n%s", seed, h, dumpState(d))
		}
	})
}

// seededRand is a minimal xorshift64* RNG, sufficient for deterministic shuffles.
type seededRand struct {
	seed uint64
}

func (r *seededRand) next() uint64 {
	r.seed ^= r.seed >> 12
	r.seed ^= r.seed << 25
	r.seed ^= r.seed >> 27
	return r.seed * 0x2545F4914F6CDD1D
}
