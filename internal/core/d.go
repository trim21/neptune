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

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"tyr/internal/meta"
	"tyr/internal/metainfo"
	"tyr/internal/pkg/bm"
	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/flowrate"
	"tyr/internal/pkg/heap"
	"tyr/internal/proto"
)

type State uint8

//go:generate stringer -type=State
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
	log zerolog.Logger

	reqLastUpdate     time.Time
	ctx               context.Context
	err               error
	cancel            context.CancelFunc
	cond              *sync.Cond
	c                 *Client
	ioDown            *flowrate.Monitor // io rate for network data and disk moving/checking data
	netDown           *flowrate.Monitor // io rate for network data
	ioUp              *flowrate.Monitor
	ResChan           chan proto.ChunkResponse
	conn              *xsync.MapOf[netip.AddrPort, *Peer]
	connectionHistory *xsync.MapOf[netip.AddrPort, connHistory]
	bm                *bm.Bitmap
	peers             *heap.Heap[peerWithPriority]
	rarePieceQueue    *heap.Heap[pieceRare] // piece index ordered by rare

	// signal to rebuild connected peers bitmap
	// should be fired when peers send bitmap/Have/HaveAll message
	buildNetworkPieces chan empty.Empty

	// signal to schedule request to peers
	// should be fired when peers finish pieces requests
	scheduleRequest chan empty.Empty

	basePath        string
	downloadDir     string
	tags            []string
	pieceInfo       []pieceFileChunks
	trackers        []TrackerTier
	pieceRare       []uint32 // mapping from piece index to rare
	pieceCount      []uint16
	chunkMap        roaring.Bitmap
	info            meta.Info
	AddAt           int64
	CompletedAt     atomic.Int64
	downloaded      atomic.Int64
	corrupted       atomic.Int64
	done            atomic.Bool
	uploaded        atomic.Int64
	uploadAtStart   int64
	downloadAtStart int64

	seq             atomic.Bool
	announcePending atomic.Bool
	m               sync.RWMutex

	ratePieceMutex sync.Mutex
	peersMutex     sync.Mutex
	reqShedMutex   sync.Mutex

	bitfieldSize uint32
	peerID       PeerID
	state        State
	private      bool
}

type pieceRare struct {
	index uint32
	rare  uint32
}

func (p pieceRare) Less(o pieceRare) bool {
	// ordered by rare 1 < 2 < 0
	if p.rare == o.rare {
		return false
	}

	if p.rare == 0 {
		return false
	}

	if o.rare == 0 {
		return true
	}

	if p.rare < o.rare {
		return true
	}

	return false
}

func (d *Download) GetState() State {
	d.m.RLock()
	s := d.state
	d.m.RUnlock()
	return s
}

func (d *Download) wait(state State) {
	d.cond.L.Lock()
	for {
		if d.state&state == 0 {
			d.cond.Wait()
			continue
		}

		break
	}
	d.cond.L.Unlock()
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
		ctx:      ctx,
		info:     info,
		cancel:   cancel,
		c:        c,
		log:      log.With().Stringer("info_hash", info.Hash).Logger(),
		state:    Checking,
		peerID:   NewPeerID(),
		tags:     tags,
		basePath: basePath,

		seq: *atomic.NewBool(true),

		AddAt: time.Now().Unix(),

		ResChan: make(chan proto.ChunkResponse, 1),

		ioDown:  flowrate.New(time.Second, time.Second),
		netDown: flowrate.New(time.Second, time.Second),
		ioUp:    flowrate.New(time.Second, time.Second),

		conn:              xsync.NewMapOf[netip.AddrPort, *Peer](),
		connectionHistory: xsync.NewMapOf[netip.AddrPort, connHistory](),

		//chunkMap: roaring.New(),

		peers: heap.New[peerWithPriority](),

		// will use about 1mb per torrent, can be optimized later
		pieceInfo: buildPieceInfos(info),

		private: info.Private,

		bm: bm.New(info.NumPieces),

		pieceCount: make([]uint16, info.NumPieces),

		bitfieldSize: (info.NumPieces + 7) / 8,

		scheduleRequest:    make(chan empty.Empty, 1),
		buildNetworkPieces: make(chan empty.Empty, 1),

		downloadDir: basePath,
	}

	d.cond = sync.NewCond(&d.m)

	d.setAnnounceList(m.UpvertedAnnounceList())

	d.log.Info().Msg("download created")

	return d
}

func (d *Download) completed() int64 {
	var completed = int64(d.bm.Count()) * d.info.PieceLength
	if d.bm.Get(d.info.NumPieces - 1) {
		completed = completed - d.info.PieceLength + d.info.LastPieceSize
	}

	return completed
}

// if download encounter an error must stop downloading/uploading
func (d *Download) setError(err error) {
	if err == io.EOF {
		panic("unexpected EOF error")
	}

	d.m.Lock()
	d.err = err
	d.state = Error
	d.m.Unlock()
}
