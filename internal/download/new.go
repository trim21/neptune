// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"fmt"
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

// InitState describes the validated state used to construct a Download.
type InitState struct {
	CompletedPieces   *bm.Bitmap
	resume            *resumeInitState
	TrackerStagger    time.Duration
	PiecePickStrategy PiecePickStrategy
	State             State
	SkipHashCheck     bool
}

type resumeInitState struct {
	addAt              time.Time
	completedAt        time.Time
	trackerKey         string
	trackers           metainfo.AnnounceList
	downloaded         int64
	uploaded           int64
	corrupted          int64
	downloadSpeedLimit int64
	uploadSpeedLimit   int64
	queueWeight        int64
}

func newSelectedFilesSet(numFiles int, selectedFiles []int) (*bm.Bitmap, error) {
	selectedFilesSet := bm.New(uint32(numFiles))
	if len(selectedFiles) == 0 {
		selectedFilesSet.Fill()
		return selectedFilesSet, nil
	}
	for _, idx := range selectedFiles {
		if idx < 0 || idx >= numFiles {
			return nil, fmt.Errorf("invalid selected file index %d", idx)
		}
		selectedFilesSet.Set(uint32(idx))
	}
	return selectedFilesSet, nil
}

func (d *Download) initializePiecePicker() {
	if d.isComplete() {
		d.picker.Store(nil)
		return
	}

	d.picker.Store(NewPiecePicker(
		d.info,
		d.missingBm,
		nil,
		&d.piecePickStrategy,
		NewRequestGate(&d.state, uint32(Downloading)),
	))
}

// New constructs a usable Download and owns all initialization goroutines.
func New(
	sess *session.Session,
	m *metainfo.MetaInfo,
	info meta.Info,
	basePath string,
	tags []string,
	custom map[string]string,
	selectedFiles []int,
	init InitState,
) (*Download, error) {
	selectedFilesSet, err := newSelectedFilesSet(len(info.Files), selectedFiles)
	if err != nil {
		return nil, err
	}
	return newDownload(sess, m, info, basePath, tags, custom, selectedFilesSet, init)
}

func newDownload(
	sess *session.Session,
	m *metainfo.MetaInfo,
	info meta.Info,
	basePath string,
	tags []string,
	custom map[string]string,
	selectedFilesSet *bm.Bitmap,
	init InitState,
) (*Download, error) {
	ctx, cancel := context.WithCancel(context.Background())

	if tags == nil {
		tags = []string{}
	}

	if custom == nil {
		custom = make(map[string]string)
	}

	completedBm := bm.New(info.NumPieces)
	if init.CompletedPieces != nil {
		completedBm.OR(init.CompletedPieces)
	}
	normalChunkLen := info.BlocksPerPiece()

	store := piece_store.NewFileStore(info, basePath, sess.FilePool, sess.IOContext, selectedFilesSet, sess.Config.App.Fallocate)

	d := &Download{
		ctx:    ctx,
		cancel: cancel,

		info:    info,
		session: sess,
		log:     log.With().Stringer("info_hash", info.Hash).Logger(),
		peerID:  NewPeerID(),

		selectedFilesSet: selectedFilesSet,

		s: downloadState{
			tags:        tags,
			custom:      custom,
			basePath:    basePath,
			downloadDir: basePath,
		},

		normalChunkLen: normalChunkLen,

		AddAt: time.Now(),

		// Default queue weight: negative timestamp (ms) so earlier-added torrents
		// have higher weight (less negative) and thus higher priority.
		queueWeight: *atomic.NewInt64(-time.Now().UnixMilli()),

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

	d.completedBm = completedBm
	d.wantedBm = bm.New(info.NumPieces)
	d.buildWantedBmUnsafe()
	d.selectedSize.Store(d.computeSelectedSizeUnsafe())

	// missingBm = wantedBm & ~completedBm. Download owns and maintains it.
	missingBm := bm.NewLockFreeBitmap(info.NumPieces)
	d.wantedBm.Range(func(i uint32) {
		if !d.completedBm.Contains(i) {
			missingBm.Set(i)
		}
	})
	d.missingBm = missingBm

	if init.PiecePickStrategy > StrategySequential {
		cancel()
		return nil, fmt.Errorf("invalid piece pick strategy %d", init.PiecePickStrategy)
	}
	d.piecePickStrategy.Store(uint32(init.PiecePickStrategy))
	d.completed.Store(d.computeCompletedUnsafe())
	d.state.Store(uint32(init.State))

	trackerKey := random.URLSafeStr(16)
	announceList := m.UpvertedAnnounceList()
	if restored := init.resume; restored != nil {
		d.AddAt = restored.addAt
		d.completedAt.Store(restored.completedAt.UnixNano())
		d.queueWeight.Store(restored.queueWeight)
		d.downloaded.Store(restored.downloaded)
		d.downloadAtStart = restored.downloaded
		d.uploaded.Store(restored.uploaded)
		d.uploadAtStart = restored.uploaded
		d.corrupted.Store(restored.corrupted)
		d.downloadLimiter.Update(restored.downloadSpeedLimit)
		d.uploadLimiter.Update(restored.uploadSpeedLimit)
		if restored.trackerKey != "" {
			trackerKey = restored.trackerKey
		}
		if len(restored.trackers) > 0 {
			announceList = restored.trackers
		}
	}

	if err := validateInitState(init.State, d.isComplete(), d.completedBm.Count()); err != nil {
		cancel()
		return nil, err
	}

	d.peerList.d = d

	d.tracker = tracker.New(d.ctx, tracker.Config{
		Key:             trackerKey,
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
	d.setAnnounceList(announceList)
	if init.TrackerStagger > 0 {
		d.TrkStagger(init.TrackerStagger)
	}

	if init.State == Checking {
		d.goBackground(func() {
			d.checkNew(init.SkipHashCheck)
			d.initializePiecePicker()
			d.startRuntime()
		})
	} else {
		d.initializePiecePicker()
		d.startRuntime()
	}

	return d, nil
}

func validateInitState(state State, complete bool, completedPieces uint32) error {
	switch state {
	case Checking:
		if completedPieces != 0 {
			return errors.New("checking download cannot start with completed pieces")
		}
	case Downloading:
		if complete {
			return errors.New("downloading download must have missing pieces")
		}
	case Seeding:
		if !complete {
			return errors.New("seeding download cannot have missing pieces")
		}
	case Stopped:
	default:
		return fmt.Errorf("invalid initial download state %s", state)
	}
	return nil
}
