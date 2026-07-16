// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"sync"
	"time"

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
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/proto"
	"neptune/internal/session"
)

const defaultBlockSize = meta.DefaultBlockSize

type State uint8

const (
	Downloading        State = 1
	PendingDownloading State = 2
	Seeding            State = 3
	Checking           State = 4
	Stopped            State = 5
	Moving             State = 6
	Error              State = 7
)

func (i State) String() string {
	switch i {
	case Stopped:
		return "Stopped"
	case Downloading:
		return "Downloading"
	case PendingDownloading:
		return "PendingDownloading"
	case Seeding:
		return "Seeding"
	case Checking:
		return "Checking"
	case Moving:
		return "Moving"
	case Error:
		return "Error"
	default:
		return "State(" + strconv.FormatUint(uint64(i), 10) + ")"
	}
}

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
		return to == Stopped || to == Seeding || to == Error || to == Checking || to == Moving || to == PendingDownloading
	case PendingDownloading:
		return to == Downloading || to == Stopped || to == Checking || to == Error
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
	AddAt                  time.Time
	ctx                    context.Context
	store                  piece_store.PieceStore
	connectedAddrs         *xsync.Map[netip.AddrPort, Peer]
	picker                 atomic.Pointer[PiecePicker]
	session                *session.Session
	pieceDownloadRate      *flowrate.Monitor
	ioDownloadRate         *flowrate.Monitor
	pieceUploadRate        *flowrate.Monitor
	resChan                chan chunkSubmit
	uploadLimiter          *ratelimit.Limiter
	peersCh                chan []tracker.DiscoveredPeer
	peers                  *xsync.Map[uint64, Peer]
	bannedAddrs            map[netip.Addr]time.Time
	stateCond              *gsync.Cond
	peerList               *peerList
	downloadLimiter        *ratelimit.Limiter
	err                    atomic.Pointer[error]
	cancel                 context.CancelFunc
	scheduleResponseSignal chan empty.Empty
	pendingPeersSignal     chan empty.Empty
	tracker                *tracker.Trackers
	completedBm            *bm.Bitmap
	missingBm              *bm.LockFreeBitmap
	wantedBm               *bm.Bitmap
	selectedFilesSet       *bm.Bitmap
	corruptedPieces        map[uint32]int
	s                      downloadState
	info                   meta.Info
	backgroundWg           sync.WaitGroup
	unchokeSlotIdx         int
	wastedDupe             atomic.Int64
	completedAt            atomic.Int64
	state                  atomic.Uint32
	downloaded             atomic.Int64
	pendingBytes           atomic.Int64
	corrupted              atomic.Int64
	uploaded               atomic.Int64
	wastedStale            atomic.Int64
	peerSeeds              atomic.Int64
	peerIDCounter          atomic.Uint64
	uploadAtStart          int64
	completed              atomic.Int64
	peerLeechers           atomic.Int64
	piecePickStrategy      atomic.Uint32
	downloadAtStart        int64
	selectedSize           atomic.Int64
	unchokeCycleOffset     int
	queueWeight            atomic.Int64
	bannedAddrsMu          sync.Mutex
	corruptedPiecesMu      sync.Mutex
	normalChunkLen         uint32
	bitfieldSize           uint32
	peerID                 proto.PeerID
	private                bool
}

// chunkSubmit wraps a chunk response with the peer that sent it, allowing
// handleRes to record contributions at reception time instead of in the peer.
type chunkSubmit struct {
	res    *proto.ChunkResponse
	peerID uint64
}

// downloadState groups mutable fields that must be accessed under s.mu.
type downloadState struct {
	custom      map[string]string
	basePath    string
	downloadDir string
	tags        []string
	mu          sync.RWMutex
}

func (d *Download) GetState() State {
	return State(d.state.Load())
}

// HasState returns true when the download is in exactly the given state.
func (d *Download) HasState(state State) bool {
	return d.GetState() == state
}

// IsDownloading returns true when the download is Downloading or PendingDownloading.
func (d *Download) IsDownloading() bool {
	s := d.GetState()
	return s == Downloading || s == PendingDownloading
}

// IsActive returns true when the download is Downloading or Seeding (actively transferring data).
func (d *Download) IsActive() bool {
	s := d.GetState()
	return s == Downloading || s == Seeding
}

// IsAlive returns true when the download is in a running state (Downloading/Seeding/PendingDownloading).
func (d *Download) IsAlive() bool {
	s := d.GetState()
	return s == Downloading || s == Seeding || s == PendingDownloading
}

// IsActiveDownloading returns true when the download is in Downloading state (not PendingDownloading, not Seeding).
func (d *Download) IsActiveDownloading() bool {
	return d.GetState() == Downloading
}

// QueueWeight returns the queue priority weight (higher = higher priority).
func (d *Download) QueueWeight() int {
	return int(d.queueWeight.Load())
}

// SetQueueWeight sets the queue priority weight.
func (d *Download) SetQueueWeight(w int) {
	d.queueWeight.Store(int64(w))
}

// DownloadRate returns the current EMA download rate in bytes/sec.
func (d *Download) DownloadRate() int64 {
	return d.pieceDownloadRate.Status().CurRate
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
	d.log.Error().Err(err).Msg("setError")

	if err == io.EOF {
		panic("unexpected EOF error")
	}

	d.err.Store(&err)
	if err := d.transition(Error); err != nil {
		d.log.Warn().Err(err).Msg("failed to transition state in setError")
	}
}

func (d *Download) isFileSelected(fileIndex int) bool {
	return d.selectedFilesSet.Contains(uint32(fileIndex))
}

// hasSelectedFiles returns true if the piece touches at least one selected file.
func (d *Download) hasSelectedFiles(pieceIndex uint32) bool {
	for chunk := range d.info.PieceFileChunks(pieceIndex) {
		if d.selectedFilesSet.Contains(uint32(chunk.FileIndex)) {
			return true
		}
	}
	return false
}

func (d *Download) SelectedSize() int64 {
	return d.selectedSize.Load()
}

// rebuildWantedState rebuilds wantedBm, selectedSize, and syncs missingBm
// after selectedFilesSet changes. Must be called under d.s.mu.
func (d *Download) rebuildWantedState() {
	d.buildWantedBmUnsafe()
	d.selectedSize.Store(d.computeSelectedSizeUnsafe())
	d.setMissingFromWantedSync()
	d.completed.Store(d.computeCompletedUnsafe())
}

// buildWantedBmUnsafe rebuilds d.wantedBm from selectedFilesSet.
// Must be called under d.s.mu.
func (d *Download) buildWantedBmUnsafe() {
	if d.selectedFilesSet.Count() == uint32(len(d.info.Files)) {
		d.wantedBm.Fill()
		return
	}
	d.wantedBm.Clear()
	for i := range d.info.NumPieces {
		if d.hasSelectedFiles(i) {
			d.wantedBm.Set(i)
		}
	}
}

func (d *Download) computeSelectedSizeUnsafe() int64 {
	done := int64(d.wantedBm.Count()) * d.info.PieceLength
	if d.wantedBm.Contains(d.info.NumPieces - 1) {
		done = done - d.info.PieceLength + d.info.LastPieceSize
	}
	return done
}

func (d *Download) computeCompletedUnsafe() int64 {
	if d.selectedFilesSet.Count() == uint32(len(d.info.Files)) {
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

// isComplete reports whether all wanted pieces are downloaded and verified.
func (d *Download) isComplete() bool {
	return d.missingBm.Count() == 0
}

// setMissingFromWantedSync syncs missingBm to wantedBm & ~completedBm.
// Must be called after any bulk change to completedBm or wantedBm that isn't
// covered by individual Set/Unset calls (e.g. OR, Fill, Clear, or wantedBm changes).
func (d *Download) setMissingFromWantedSync() {
	d.missingBm.Clear()
	d.wantedBm.Range(func(i uint32) {
		if !d.completedBm.Contains(i) {
			d.missingBm.Set(i)
		}
	})
}

func (d *Download) peerSeedLeecherCounts() (seeds, leechers int) {
	return int(d.peerSeeds.Load()), int(d.peerLeechers.Load())
}

// recalcPeerCounts iterates all connected peers and refreshes the cached seed/leecher counters.
func (d *Download) recalcPeerCounts() {
	var seeds, leechers int64
	d.peers.Range(func(_ uint64, p Peer) bool {
		if p.IsSeed() {
			seeds++
		} else {
			leechers++
		}
		return true
	})
	d.peerSeeds.Store(seeds)
	d.peerLeechers.Store(leechers)
}
