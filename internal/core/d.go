// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

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

// Download manage a download task
// ctx should be canceled when torrent is removed, not stopped.
type Download struct {
	log                    zerolog.Logger
	ctx                    context.Context
	err                    error
	cancel                 context.CancelFunc
	c                      *Client
	ioDown                 *flowrate.Monitor
	netDown                *flowrate.Monitor
	ioUp                   *flowrate.Monitor
	ResChan                chan *proto.ChunkResponse
	downloadLimiter        *ratelimit.Limiter
	uploadLimiter          *ratelimit.Limiter
	peers                  *xsync.Map[netip.AddrPort, *Peer]
	connectionHistory      *expirable.LRU[netip.AddrPort, connHistory]
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
	trackerKey             string
	chunkHeap              heap.Heap[responseChunk]
	tags                   []string
	pieceInfo              []pieceFileChunks
	trackers               []TrackerTier
	pieceAvailability      []int32
	chunkMap               bitmap.Bitmap
	pendingChunksMap       bitmap.Bitmap
	info                   meta.Info
	completed              atomic.Int64
	selectedSize           atomic.Int64
	AddAt                  int64
	CompletedAt            atomic.Int64
	downloaded             atomic.Int64
	corrupted              atomic.Int64
	uploaded               atomic.Int64
	uploadAtStart          int64
	downloadAtStart        int64
	endGameMode            atomic.Bool
	seq                    atomic.Bool
	announcePending        atomic.Bool
	trackerMutex           sync.RWMutex
	m                      sync.RWMutex
	ratePieceMutex         sync.Mutex
	pendingPeersMutex      sync.Mutex
	normalChunkLen         uint32
	bitfieldSize           uint32
	peerID                 proto.PeerID
	state                  State
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

func (d *Download) GetState() State {
	d.m.RLock()
	s := d.state
	d.m.RUnlock()
	return s
}

// HasState returns true if the download is in any of the given states.
func (d *Download) HasState(state State) bool {
	return d.GetState()&state != 0
}

var ErrTorrentNotFound = errors.New("torrent not found")

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

func (c *Client) NewDownload(m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string, selectedFiles []int) *Download {
	ctx, cancel := context.WithCancel(context.Background())

	if tags == nil {
		tags = []string{}
	}

	d := &Download{
		ctx:    ctx,
		cancel: cancel,

		info:     info,
		c:        c,
		log:      log.With().Stringer("info_hash", info.Hash).Logger(),
		state:    Checking,
		peerID:   NewPeerID(),
		tags:     tags,
		basePath: basePath,

		normalChunkLen: as.Uint32((info.PieceLength + defaultBlockSize - 1) / defaultBlockSize),

		seq: *atomic.NewBool(true),

		AddAt: time.Now().Unix(),

		ResChan: make(chan *proto.ChunkResponse, 1),

		ioDown:  flowrate.New(time.Second, time.Second),
		netDown: flowrate.New(time.Second, time.Second),
		ioUp:    flowrate.New(time.Second, time.Second),

		peers:             xsync.NewMap[netip.AddrPort, *Peer](),
		connectionHistory: expirable.NewLRU[netip.AddrPort, connHistory](1024, nil, time.Minute*10),

		chunkMap:         make(bitmap.Bitmap, int64(info.NumPieces)*((info.PieceLength+defaultBlockSize-1)/defaultBlockSize)),
		endgameRequested: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		pendingPeers: heap.New[peerWithPriority](),

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

		trackerKey: random.URLSafeStr(16),

		downloadLimiter: ratelimit.New(0),
		uploadLimiter:   ratelimit.New(0),
	}

	if selectedFiles != nil {
		d.selectedFilesSet = make(map[int]struct{}, len(selectedFiles))
		for _, idx := range selectedFiles {
			if idx >= 0 && idx < len(info.Files) {
				d.selectedFilesSet[idx] = struct{}{}
			}
		}
	}
	d.selectedSize.Store(d.computeSelectedSize())
	d.buildSelectedPiecesBm()

	d.stateCond = gsync.NewCond(&d.m)

	d.setAnnounceList(m.UpvertedAnnounceList())

	d.log.Info().Msg("download created")

	return d
}

// if download encounter an error must stop downloading/uploading.
func (d *Download) setError(err error) {
	if err == io.EOF {
		panic("unexpected EOF error")
	}

	d.m.Lock()
	d.err = err
	d.state = Error
	d.m.Unlock()
}

// hasSelectedFiles returns true if the piece touches at least one selected file.
func (d *Download) hasSelectedFiles(pieceIndex uint32) bool {
	if d.selectedFilesSet == nil {
		return true
	}
	for _, c := range d.pieceInfo[pieceIndex].fileChunks {
		if _, ok := d.selectedFilesSet[c.fileIndex]; ok {
			return true
		}
	}
	return false
}

func (d *Download) SelectedSize() int64 {
	return d.selectedSize.Load()
}

func (d *Download) computeSelectedSize() int64 {
	if d.selectedFilesSet == nil {
		return d.info.TotalLength
	}
	var size int64
	for idx := range d.selectedFilesSet {
		size += d.info.Files[idx].Length
	}
	return size
}

func (d *Download) buildSelectedPiecesBm() {
	if d.selectedPiecesBm == nil {
		d.selectedPiecesBm = bm.New(d.info.NumPieces)
	}
	if d.selectedFilesSet == nil {
		d.selectedPiecesBm.Fill()
		return
	}
	d.selectedPiecesBm.Clear()
	for i := range d.info.NumPieces {
		if d.hasSelectedFiles(i) {
			d.selectedPiecesBm.Set(i)
		}
	}
}

func (d *Download) computeCompleted() int64 {
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

// markUnselectedPiecesDone marks pieces that only touch unselected files as complete.
func (d *Download) markUnselectedPiecesDone() {
	if d.selectedFilesSet == nil {
		return
	}
	for i := range d.info.NumPieces {
		if !d.selectedPiecesBm.Contains(i) {
			d.bm.Set(i)
		}
	}
}
