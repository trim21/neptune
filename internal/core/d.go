// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"go.uber.org/atomic"

	"neptune/internal/core/tracker"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/proto"
)

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
	ctx                    context.Context
	err                    atomic.Pointer[error]
	cancel                 context.CancelFunc
	c                      *Client
	pieceDownloadRate      *flowrate.Monitor
	ioDownloadRate         *flowrate.Monitor
	pieceUploadRate        *flowrate.Monitor
	ResChan                chan *proto.ChunkResponse
	downloadLimiter        *ratelimit.Limiter
	uploadLimiter          *ratelimit.Limiter
	peers                  *xsync.Map[netip.AddrPort, *Peer]
	connectionHistory      *lru.Cache[netip.AddrPort, connHistory]
	bm                     *bm.Bitmap
	selectedPiecesBm       *bm.Bitmap
	pendingPeers           *heap.Heap[peerWithPriority]
	rarePieceQueue         *heap.Heap[pieceRare]
	buildNetworkPieces     chan empty.Empty
	scheduleRequestSignal  chan empty.Empty
	scheduleResponseSignal chan empty.Empty
	pendingPeersSignal     chan empty.Empty
	stateCond              *gsync.Cond
	pexAdd                 chan []pexPeer
	pexDrop                chan []netip.AddrPort
	endgameRequested       *xsync.Map[proto.ChunkRequest, empty.Empty]
	selectedFilesSet       map[int]struct{}
	basePath               string
	downloadDir            string
	chunk                  chunkState
	tags                   []string
	custom                 map[string]string
	pieceInfo              pieceInfo
	Trk                    *tracker.Trackers
	pieceAvailability      []int32
	info                   meta.Info
	completed              atomic.Int64
	selectedSize           atomic.Int64
	AddAt                  int64
	CompletedAt            atomic.Int64
	downloaded             atomic.Int64
	corrupted              atomic.Int64
	corruptedBytes         atomic.Int64 // bytes received for pieces that failed hash check
	uploaded               atomic.Int64
	uploadAtStart          int64
	downloadAtStart        int64
	endGameMode            atomic.Bool
	seq                    atomic.Bool
	peerSeeds              atomic.Int64
	peerLeechers           atomic.Int64
	state                  atomic.Uint32
	m                      sync.RWMutex
	backgroundWg           sync.WaitGroup
	ratePieceMutex         sync.Mutex
	pendingPeersMutex      sync.Mutex
	unchokeSlotIdx         int
	normalChunkLen         uint32
	bitfieldSize           uint32
	peerID                 proto.PeerID
	private                bool
}

type pieceRare struct {
	index    uint32
	priority int32
}

func (p pieceRare) Less(o pieceRare) bool {
	if p.priority == o.priority {
		return p.index < o.index
	}
	// higher priority first
	return p.priority > o.priority
}

type chunkState struct {
	heap    heap.Heap[responseChunk]
	done    bitmap.Bitmap
	pending bitmap.Bitmap
	mu      sync.RWMutex
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

func (c *Client) ScheduleMove(ih metainfo.Hash, targetBasePath string) error {
	c.m.RLock()
	d, ok := c.downloadMap[ih]
	c.m.RUnlock()

	if !ok {
		return ErrTorrentNotFound
	}

	err := d.Move(targetBasePath)

	return err
}

func (c *Client) NewDownload(m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string, custom map[string]string, selectedFiles []int) *Download {
	ctx, cancel := context.WithCancel(context.Background())

	if tags == nil {
		tags = []string{}
	}

	if custom == nil {
		custom = make(map[string]string)
	}

	d := &Download{
		ctx:    ctx,
		cancel: cancel,

		info:     info,
		c:        c,
		log:      log.With().Stringer("info_hash", info.Hash).Logger(),
		peerID:   NewPeerID(),
		tags:     tags,
		custom:   custom,
		basePath: basePath,

		normalChunkLen: as.Uint32((info.PieceLength + defaultBlockSize - 1) / defaultBlockSize),

		seq: *atomic.NewBool(true),

		AddAt: time.Now().Unix(),

		ResChan: make(chan *proto.ChunkResponse, 1),

		pieceDownloadRate: flowrate.New(time.Second, time.Second),
		ioDownloadRate:    flowrate.New(time.Second, time.Second),
		pieceUploadRate:   flowrate.New(time.Second, time.Second),

		peers:             xsync.NewMap[netip.AddrPort, *Peer](),
		connectionHistory: lo.Must(lru.New[netip.AddrPort, connHistory](256)),

		chunk: chunkState{
			done: make(bitmap.Bitmap, (int64(info.NumPieces)*((info.PieceLength+defaultBlockSize-1)/defaultBlockSize)+63)/64),
		},
		endgameRequested: xsync.NewMap[proto.ChunkRequest, empty.Empty](),
		pendingPeers:     heap.New[peerWithPriority](),

		// will use about 1mb per torrent, can be optimized later
		pieceInfo: buildPieceInfos(info),

		private: info.Private,

		bm: bm.New(info.NumPieces),

		bitfieldSize: (info.NumPieces + 7) / 8,

		scheduleRequestSignal:  make(chan empty.Empty, 1),
		scheduleResponseSignal: make(chan empty.Empty, 1),
		pendingPeersSignal:     make(chan empty.Empty),
		buildNetworkPieces:     make(chan empty.Empty, 1),

		pexAdd:  make(chan []pexPeer, 1),
		pexDrop: make(chan []netip.AddrPort, 1),

		downloadDir: basePath,

		downloadLimiter: ratelimit.New(0),
		uploadLimiter:   ratelimit.New(0),
	}

	d.state.Store(uint32(Checking))

	if selectedFiles != nil {
		d.selectedFilesSet = make(map[int]struct{}, len(selectedFiles))
		for _, idx := range selectedFiles {
			if idx >= 0 && idx < len(info.Files) {
				d.selectedFilesSet[idx] = struct{}{}
			}
		}
	}
	d.selectedSize.Store(d.computeSelectedSizeUnsafe())
	d.buildSelectedPiecesBmUnsafe()

	d.Trk = tracker.New(d.ctx, tracker.Config{
		Key:             random.URLSafeStr(16),
		HTTP:            c.http,
		InfoHash:        info.Hash.AsString(),
		PeerID:          d.peerID.AsString(),
		Port:            c.Config.App.P2PPort,
		Uploaded:        &d.uploaded,
		UploadedStart:   d.uploadAtStart,
		Downloaded:      &d.downloaded,
		DownloadedStart: d.downloadAtStart,
		Completed:       &d.completed,
		SelectedSize:    &d.selectedSize,
		Debug:           c.debug,
		OnPeers: func(peers []netip.AddrPort) {
			d.pendingPeersMutex.Lock()
			for _, p := range peers {
				d.pendingPeers.Push(peerWithPriority{
					addrPort: p,
					priority: c.PeerPriority(p),
				})
			}
			d.pendingPeersMutex.Unlock()

			select {
			case d.pendingPeersSignal <- empty.Empty{}:
			default:
			}
		},
	})

	d.stateCond = gsync.NewCond(&gsync.EmptyLock{})

	d.setAnnounceList(m.UpvertedAnnounceList())

	d.log.Info().Msg("download created")

	return d
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
	if d.selectedFilesSet == nil {
		return true
	}
	_, ok := d.selectedFilesSet[fileIndex]
	return ok
}

// hasSelectedFilesUnsafe returns true if the piece touches at least one selected file.
func (d *Download) hasSelectedFilesUnsafe(pieceIndex uint32) bool {
	if d.selectedFilesSet == nil {
		return true
	}
	for _, c := range d.pieceInfo.fileChunks(pieceIndex) {
		if _, ok := d.selectedFilesSet[c.fileIndex]; ok {
			return true
		}
	}
	return false
}

func (d *Download) SelectedSize() int64 {
	return d.selectedSize.Load()
}

func (d *Download) computeSelectedSizeUnsafe() int64 {
	if d.selectedFilesSet == nil {
		return d.info.TotalLength
	}
	var size int64
	for idx := range d.selectedFilesSet {
		size += d.info.Files[idx].Length
	}
	return size
}

func (d *Download) buildSelectedPiecesBmUnsafe() {
	if d.selectedPiecesBm == nil {
		d.selectedPiecesBm = bm.New(d.info.NumPieces)
	}
	if d.selectedFilesSet == nil {
		d.selectedPiecesBm.Fill()
		return
	}
	d.selectedPiecesBm.Clear()
	for i := range d.info.NumPieces {
		if d.hasSelectedFilesUnsafe(i) {
			d.selectedPiecesBm.Set(i)
		}
	}
}

func (d *Download) computeCompletedUnsafe() int64 {
	if d.selectedFilesSet == nil {
		done := int64(d.bm.Count()) * d.info.PieceLength
		if d.bm.Contains(d.info.NumPieces - 1) {
			done = done - d.info.PieceLength + d.info.LastPieceSize
		}
		return done
	}
	done := int64(d.bm.WithAnd(d.selectedPiecesBm).Count()) * d.info.PieceLength
	if d.bm.Contains(d.info.NumPieces-1) && d.selectedPiecesBm.Contains(d.info.NumPieces-1) {
		done = done - d.info.PieceLength + d.info.LastPieceSize
	}
	return done
}

// markUnselectedPiecesDoneUnsafe marks pieces that only touch unselected files as complete.
func (d *Download) markUnselectedPiecesDoneUnsafe() {
	if d.selectedFilesSet == nil {
		return
	}
	for i := range d.info.NumPieces {
		if !d.selectedPiecesBm.Contains(i) {
			d.bm.Set(i)
		}
	}
}

func (d *Download) peerSeedLeecherCounts() (seeds, leechers int) {
	return int(d.peerSeeds.Load()), int(d.peerLeechers.Load())
}

// recalcPeerCounts iterates all connected peers and refreshes the cached seed/leecher counters.
func (d *Download) recalcPeerCounts() {
	var seeds, leechers int64
	d.peers.Range(func(_ netip.AddrPort, p *Peer) bool {
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
