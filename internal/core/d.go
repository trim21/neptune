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
	log               zerolog.Logger
	ctx               context.Context
	err               error
	cancel            context.CancelFunc
	c                 *Client
	ioDown            *flowrate.Monitor // io rate for network data and disk moving/checking data
	netDown           *flowrate.Monitor // io rate for network data
	ioUp              *flowrate.Monitor
	ResChan           chan *proto.ChunkResponse
	peers             *xsync.Map[netip.AddrPort, *Peer]
	connectionHistory *expirable.LRU[netip.AddrPort, connHistory]
	bm                *bm.Bitmap
	pendingPeers      *heap.Heap[peerWithPriority]
	rarePieceQueue    *heap.Heap[pieceRare] // piece index ordered by rare

	// signal to rebuild connected pendingPeers bitmap
	// should be fired when pendingPeers send bitmap/Have/HaveAll message
	buildNetworkPieces chan empty.Empty

	// signal to schedule request to pendingPeers
	// should be fired when pendingPeers finish pieces requests
	scheduleRequestSignal chan empty.Empty

	scheduleResponseSignal chan empty.Empty
	pendingPeersSignal     chan empty.Empty
	stateCond              *gsync.Cond
	pexAdd                 chan []pexPeer
	pexDrop                chan []netip.AddrPort
	basePath               string
	downloadDir            string
	trackerKey             string
	chunkHeap              heap.Heap[responseChunk]
	tags                   []string
	pieceInfo              []pieceFileChunks
	trackers               []TrackerTier
	pieceRare              []uint32 // mapping from piece index to rare
	chunkMap               bitmap.Bitmap
	pendingChunksMap       bitmap.Bitmap
	info                   meta.Info
	completed              atomic.Int64
	endGameMode            atomic.Bool
	AddAt                  int64
	CompletedAt            atomic.Int64
	downloaded             atomic.Int64
	corrupted              atomic.Int64
	uploaded               atomic.Int64
	uploadAtStart          int64
	downloadAtStart        int64
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
	index uint32
	rare  uint32
}

func (p pieceRare) Less(o pieceRare) bool {
	// ordered by rare 1 < 2 < 0
	if p.rare == o.rare {
		return p.index < o.index
	}

	if p.rare == 0 {
		return false
	}

	if o.rare == 0 {
		return true
	}

	return p.rare < o.rare
}

func (d *Download) GetState() State {
	d.m.RLock()
	s := d.state
	d.m.RUnlock()
	return s
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

func (c *Client) NewDownload(m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string) *Download {
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

		chunkMap: make(bitmap.Bitmap, int64(info.NumPieces)*((info.PieceLength+defaultBlockSize-1)/defaultBlockSize)),

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
	}

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
