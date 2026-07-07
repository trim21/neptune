package download

import (
	"context"
	"net/netip"
	"time"

	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/proto"
	"neptune/internal/session"
)

// New creates a new Download.
func New(sess *session.Session, m *metainfo.MetaInfo, info meta.Info, basePath string, tags []string, custom map[string]string, selectedFiles []int) *Download {
	ctx, cancel := context.WithCancel(context.Background())

	if tags == nil {
		tags = []string{}
	}

	if custom == nil {
		custom = make(map[string]string)
	}

	completedBm := bm.New(info.NumPieces)

	store := piece_store.NewFileStore(info, basePath, sess.FilePool)

	d := &Download{
		ctx:    ctx,
		cancel: cancel,

		info:    info,
		session: sess,
		log:     log.With().Stringer("info_hash", info.Hash).Logger(),
		peerID:  NewPeerID(),

		s: downloadState{
			tags:        tags,
			custom:      custom,
			basePath:    basePath,
			downloadDir: basePath,
		},

		normalChunkLen: as.Uint32((info.PieceLength + defaultBlockSize - 1) / defaultBlockSize),

		AddAt: time.Now().Unix(),

		resChan: make(chan *proto.ChunkResponse, 1),

		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		ioDownloadRate:    flowrate.New(time.Second, 5*time.Second),
		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),

		peers:          xsync.NewMap[uint64, *Peer](),
		connectedAddrs: xsync.NewMap[netip.AddrPort, *Peer](),
		peerList:       newPeerList(nil), // d set below

		picker: newPiecePicker(info, completedBm),

		store: store,

		chunk: chunkState{
			done: make(bitmap.Bitmap, (int64(info.NumPieces)*((info.PieceLength+defaultBlockSize-1)/defaultBlockSize)+63)/64),
		},

		pieceInfo: piece_store.BuildPieceInfos(info),

		private: info.Private,

		bitfieldSize: (info.NumPieces + 7) / 8,

		scheduleRequestSignal:  make(chan empty.Empty, 1),
		scheduleResponseSignal: make(chan empty.Empty, 1),
		pendingPeersSignal:     make(chan empty.Empty),

		pexAdd:  make(chan []pexPeer, 1),
		pexDrop: make(chan []netip.AddrPort, 1),

		downloadLimiter: ratelimit.New(0),
		uploadLimiter:   ratelimit.New(0),

		selectedSize: *atomic.NewInt64(info.TotalLength),
	}

	d.completedBm = completedBm
	d.wantedBm = bm.New(info.NumPieces)

	// mark all pieces as wanted initially
	for i := range info.NumPieces {
		d.wantedBm.Set(i)
	}

	d.peerList.d = d

	d.Trk = tracker.New(d.ctx, tracker.Config{
		Key:             random.URLSafeStr(16),
		HTTP:            sess.HTTP,
		InfoHash:        info.Hash.AsString(),
		PeerID:          d.peerID.AsString(),
		Port:            sess.Config.App.P2PPort,
		Uploaded:        &d.uploaded,
		UploadedStart:   d.uploadAtStart,
		Downloaded:      &d.downloaded,
		DownloadedStart: d.downloadAtStart,
		Completed:       &d.completed,
		SelectedSize:    &d.selectedSize,
		Debug:           sess.Debug,
		OnPeers: func(peers []netip.AddrPort) {
			for _, addr := range peers {
				d.peerList.addPeer(addr, peerSourceTracker, true)
			}
			select {
			case d.pendingPeersSignal <- empty.Empty{}:
			default:
			}
		},
	})

	d.stateCond = gsync.NewCond(&gsync.EmptyLock{})

	d.setAnnounceList(m.UpvertedAnnounceList())

	return d
}
