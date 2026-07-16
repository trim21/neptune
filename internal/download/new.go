// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"net/netip"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"neptune/internal/client/tracker"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/random"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/session"
)

// defaultStrategy returns the client-level default piece pick strategy.
// Falls back to rarest-first when the config value is empty or unrecognized.
func defaultStrategy(cfg string) PiecePickStrategy {
	s, err := PiecePickStrategyFromString(cfg)
	if err != nil {
		return StrategyRarestFirst
	}
	return s
}

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

	normalChunkLen := info.BlocksPerPiece()

	store := piece_store.NewFileStore(info, basePath, sess.FilePool, sess.IOContext)

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

		normalChunkLen: normalChunkLen,

		AddAt: time.Now().Unix(),

		// Default queue weight: negative timestamp so earlier-added torrents
		// have higher weight (less negative) and thus higher priority.
		queueWeight: *atomic.NewInt64(-time.Now().Unix()),

		resChan: make(chan chunkSubmit, 1),

		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		ioDownloadRate:    flowrate.New(time.Second, 5*time.Second),
		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),

		peers:           xsync.NewMap[uint64, Peer](),
		connectedAddrs:  xsync.NewMap[netip.AddrPort, Peer](),
		peerList:        newPeerList(nil), // d set below
		corruptedPieces: make(map[uint32]int),
		bannedAddrs:     make(map[netip.Addr]time.Time),

		store: store,

		private: info.Private,

		bitfieldSize: (info.NumPieces + 7) / 8,

		scheduleResponseSignal: make(chan empty.Empty, 1),
		pendingPeersSignal:     make(chan empty.Empty, 1),

		downloadLimiter: ratelimit.New(0),
		uploadLimiter:   ratelimit.New(0),

		peersCh: make(chan []tracker.DiscoveredPeer, 1),
	}

	// Populate selectedFilesSet if only a subset of files is selected.
	// nil means all files are selected.
	if len(selectedFiles) > 0 && len(selectedFiles) < len(info.Files) {
		d.s.selectedFilesSet = make(map[int]struct{}, len(selectedFiles))
		for _, idx := range selectedFiles {
			d.s.selectedFilesSet[idx] = struct{}{}
		}
	}

	d.completedBm = completedBm
	d.wantedBm = bm.New(info.NumPieces)

	d.selectedSize = *atomic.NewInt64(d.computeSelectedSizeUnsafe())

	// missingBm = wantedBm & ~completedBm. Download owns and maintains it.
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	d.wantedBm.Range(func(i uint32) {
		if !d.completedBm.Contains(i) {
			missingBm.Set(i)
		}
	})
	d.missingBm = missingBm

	strategy := defaultStrategy(sess.Config.App.PiecePickStrategy)
	d.piecePickStrategy.Store(uint32(strategy))
	d.picker.Store(NewPiecePicker(info, missingBm, nil, &d.piecePickStrategy))

	d.peerList.d = d

	d.tracker = tracker.New(d.ctx, tracker.Config{
		Key:             random.URLSafeStr(16),
		HTTP:            sess.HTTP,
		Log:             d.log,
		InfoHash:        info.Hash.AsString(),
		PeerID:          d.peerID.AsString(),
		Port:            sess.Config.App.P2PPort,
		Uploaded:        &d.uploaded,
		UploadedStart:   d.uploadAtStart,
		Downloaded:      &d.downloaded,
		DownloadedStart: d.downloadAtStart,
		Completed:       &d.completed,
		SelectedSize:    &d.selectedSize,
		NumWant:         200,
		Debug:           sess.Debug,
		PeersCh:         d.peersCh,
	})

	d.stateCond = gsync.NewCond(&gsync.EmptyLock{})

	d.setAnnounceList(m.UpvertedAnnounceList())

	return d
}
