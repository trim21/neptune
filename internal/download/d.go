// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"

	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
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

const defaultBlockSize = meta.DefaultBlockSize

type State uint8

//go:generate go tool golang.org/x/tools/cmd/stringer -type=State
const (
	Stopped     State = 1 << 0
	Downloading State = 1 << 1
	Seeding     State = 1 << 2
	Checking    State = 1 << 3
	Moving      State = 1 << 4
	Error       State = 1 << 5
)

type TransitionError struct {
	From State
	To   State
}

func (e *TransitionError) Error() string {
	if msg := e.userMessage(); msg != "" {
		return msg
	}
	return fmt.Sprintf("invalid state transition from %s to %s", e.From, e.To)
}

func (e *TransitionError) userMessage() string {
	switch {
	case e.From == Moving && e.To == Moving:
		return "torrent is already being moved"
	case e.From == Moving && e.To == Checking:
		return "torrent is being moved, cannot recheck"
	case e.From == Checking && e.To == Moving:
		return "torrent is being rechecked, cannot move"
	case e.From == Checking && e.To == Checking:
		return "torrent is already being rechecked"
	}
	return ""
}

func (d *Download) transition(to State) error {
	for {
		old := State(d.state.Load())
		if old == to {
			return nil
		}
		if !validTransition(old, to) {
			return &TransitionError{From: old, To: to}
		}
		if d.state.CompareAndSwap(uint32(old), uint32(to)) {
			return nil
		}
	}
}

func validTransition(from, to State) bool {
	switch from {
	case Stopped:
		return to == Downloading || to == Seeding || to == Checking || to == Moving
	case Downloading:
		return to == Stopped || to == Seeding || to == Error || to == Checking || to == Moving
	case Seeding:
		return to == Stopped || to == Error || to == Checking || to == Moving
	case Checking:
		return to == Downloading || to == Seeding || to == Error
	case Moving:
		return to == Downloading || to == Seeding || to == Stopped || to == Error
	case Error:
		return to == Checking
	}
	return false
}

// Download manage a download task
// ctx should be canceled when torrent is removed, not stopped.
type Download struct {
	log                    zerolog.Logger
	s                      downloadState
	ctx                    context.Context
	store                  piece_store.PieceStore
	corruptedPieces        map[uint32]int
	downloadLimiter        *ratelimit.Limiter
	session                *session.Session
	pieceDownloadRate      *flowrate.Monitor
	ioDownloadRate         *flowrate.Monitor
	pieceUploadRate        *flowrate.Monitor
	resChan                chan *proto.ChunkResponse
	uploadLimiter          *ratelimit.Limiter
	peersCh                chan []discoveredPeer
	peers                  *xsync.Map[uint64, *Peer]
	connectedAddrs         *xsync.Map[netip.AddrPort, *Peer]
	stateCond              *gsync.Cond
	peerList               *peerList
	picker                 atomic.Pointer[piecePicker]
	err                    atomic.Pointer[error]
	cancel                 context.CancelFunc
	scheduleRequestSignal  chan empty.Empty
	scheduleResponseSignal chan empty.Empty
	pendingPeersSignal     chan empty.Empty
	Trk                    *tracker.Trackers
	completedBm            *bm.Bitmap
	wantedBm               *bm.Bitmap
	pieceInfo              meta.PieceInfo
	chunk                  chunkState
	info                   meta.Info
	backgroundWg           sync.WaitGroup
	completed              atomic.Int64
	CompletedAt            atomic.Int64
	state                  atomic.Uint32
	downloaded             atomic.Int64
	corrupted              atomic.Int64
	uploaded               atomic.Int64
	peerIDCounter          atomic.Uint64
	uploadAtStart          int64
	unchokeSlotIdx         int
	AddAt                  int64
	peerLeechers           atomic.Int64
	peerSeeds              atomic.Int64
	downloadAtStart        int64
	selectedSize           atomic.Int64
	unchokeCycleOffset     int
	corruptedPiecesMu      sync.Mutex
	normalChunkLen         uint32
	bitfieldSize           uint32
	peerID                 proto.PeerID
	private                bool
}

type chunkState struct {
	heap    heap.Heap[responseChunk]
	done    bitmap.Bitmap
	pending bitmap.Bitmap
	mu      sync.RWMutex
}

// downloadState groups mutable fields that must be accessed under s.mu.
type downloadState struct {
	custom           map[string]string
	selectedFilesSet map[int]struct{}
	basePath         string
	downloadDir      string
	tags             []string
	mu               sync.RWMutex
}

func (d *Download) GetState() State {
	return State(d.state.Load())
}

// HasState returns true if the download is in any of the given states.
func (d *Download) HasState(state State) bool {
	return d.GetState()&state != 0
}

var ErrTorrentNotFound = errors.New("torrent not found")

func (d *Download) ErrorMsg() string {
	if e := d.err.Load(); e != nil {
		return (*e).Error()
	}
	return ""
}

// if download encounter an error must stop downloading/uploading.
func (d *Download) setError(err error) {
	if err == io.EOF {
		panic("unexpected EOF error")
	}

	d.err.Store(&err)
	if err := d.transition(Error); err != nil {
		d.log.Warn().Err(err).Msg("failed to transition state in setError")
	}
}

func (d *Download) isFileSelected(fileIndex int) bool {
	if d.s.selectedFilesSet == nil {
		return true
	}
	_, ok := d.s.selectedFilesSet[fileIndex]
	return ok
}

// hasSelectedFilesUnsafe returns true if the piece touches at least one selected file.
func (d *Download) hasSelectedFilesUnsafe(pieceIndex uint32) bool {
	if d.s.selectedFilesSet == nil {
		return true
	}
	for _, c := range d.pieceInfo.FileChunks(pieceIndex) {
		if _, ok := d.s.selectedFilesSet[c.FileIndex]; ok {
			return true
		}
	}
	return false
}

func (d *Download) SelectedSize() int64 {
	return d.selectedSize.Load()
}

func (d *Download) computeSelectedSizeUnsafe() int64 {
	if d.s.selectedFilesSet == nil {
		return d.info.TotalLength
	}
	var size int64
	for idx := range d.s.selectedFilesSet {
		size += d.info.Files[idx].Length
	}
	return size
}

func (d *Download) buildSelectedPiecesBmUnsafe() {
	if d.wantedBm == nil {
		d.wantedBm = bm.New(d.info.NumPieces)
	}
	if d.s.selectedFilesSet == nil {
		d.wantedBm.Fill()
		return
	}
	d.wantedBm.Clear()
	for i := range d.info.NumPieces {
		if d.hasSelectedFilesUnsafe(i) {
			d.wantedBm.Set(i)
		}
	}
}

func (d *Download) computeCompletedUnsafe() int64 {
	if d.s.selectedFilesSet == nil {
		done := int64(d.completedBm.Count()) * d.info.PieceLength
		if d.completedBm.Contains(d.info.NumPieces - 1) {
			done = done - d.info.PieceLength + d.info.LastPieceSize
		}
		return done
	}
	done := int64(d.completedBm.WithAnd(d.wantedBm).Count()) * d.info.PieceLength
	if d.completedBm.Contains(d.info.NumPieces-1) && d.wantedBm.Contains(d.info.NumPieces-1) {
		done = done - d.info.PieceLength + d.info.LastPieceSize
	}
	return done
}

// markUnselectedPiecesDoneUnsafe marks pieces that only touch unselected files as complete.
func (d *Download) markUnselectedPiecesDoneUnsafe() {
	if d.s.selectedFilesSet == nil {
		return
	}

	// unwanted = NOT wantedBm — compute via full.WithAndNot(wantedBm)
	full := bm.New(d.info.NumPieces)
	full.Fill()
	unwanted := full.WithAndNot(d.wantedBm)

	d.completedBm.OR(unwanted)

	unwanted.Range(func(i uint32) {
		d.picker.Load().weHave(i, d.info)
	})
}

func (d *Download) peerSeedLeecherCounts() (seeds, leechers int) {
	return int(d.peerSeeds.Load()), int(d.peerLeechers.Load())
}

// recalcPeerCounts iterates all connected peers and refreshes the cached seed/leecher counters.
func (d *Download) recalcPeerCounts() {
	var seeds, leechers int64
	d.peers.Range(func(_ uint64, p *Peer) bool {
		if p.isSeed.Load() {
			seeds++
		} else {
			leechers++
		}
		return true
	})
	d.peerSeeds.Store(seeds)
	d.peerLeechers.Store(leechers)
}
