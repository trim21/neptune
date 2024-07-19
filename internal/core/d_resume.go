// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"encoding"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/hashicorp/golang-lru/v2/expirable"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"go.uber.org/atomic"

	"tyr/internal/meta"
	"tyr/internal/metainfo"
	"tyr/internal/pkg/bm"
	"tyr/internal/pkg/flowrate"
	"tyr/internal/pkg/gsync"
	"tyr/internal/pkg/heap"
	"tyr/internal/proto"
)

var _ encoding.BinaryMarshaler = (*Download)(nil)

type resume struct {
	BasePath    string
	Bitmap      []byte
	Tags        []string
	Trackers    [][]string
	AddAt       int64
	CompletedAt int64
	Downloaded  int64
	Uploaded    int64
	State       State
}

func (d *Download) MarshalBinary() (data []byte, err error) {
	return bencode.Marshal(resume{
		BasePath:    d.basePath,
		Downloaded:  d.downloaded.Load(),
		Uploaded:    d.uploaded.Load(),
		Tags:        d.tags,
		State:       d.state,
		AddAt:       d.AddAt,
		CompletedAt: d.CompletedAt.Load(),
		Trackers: lo.Map(d.trackers, func(tier TrackerTier, index int) []string {
			return lo.Map(tier.trackers, func(tracker *Tracker, index int) string {
				return tracker.url
			})
		}),
	})
}

func (c *Client) UnmarshalResume(data []byte, torrentDirectory string) (*Download, error) {
	var r resume
	if err := bencode.Unmarshal(data, &r); err != nil {
		return nil, errgo.Wrap(err, "failed to decode resume data")
	}

	var m, err = metainfo.LoadFromFile(filepath.Join(torrentDirectory, ""))
	if err != nil {
		return nil, errgo.Wrap(err, "failed to decode torrent data")
	}

	info, err := meta.FromTorrent(*m)
	if err != nil {
		return nil, errgo.Wrap(err, "failed to decode torrent data")
	}

	ctx, cancel := context.WithCancel(context.Background())

	d := &Download{
		CompletedAt: *atomic.NewInt64(r.CompletedAt),

		ctx:      ctx,
		info:     info,
		cancel:   cancel,
		c:        c,
		log:      log.With().Stringer("info_hash", info.Hash).Logger(),
		state:    Checking,
		peerID:   NewPeerID(),
		tags:     r.Tags,
		basePath: r.BasePath,

		AddAt: time.Now().Unix(),

		ResChan: make(chan *proto.ChunkResponse, 1),

		ioDown:  flowrate.New(time.Second, time.Second),
		netDown: flowrate.New(time.Second, time.Second),
		ioUp:    flowrate.New(time.Second, time.Second),

		peers:             xsync.NewMapOf[netip.AddrPort, *Peer](),
		connectionHistory: expirable.NewLRU[netip.AddrPort, connHistory](1024, nil, time.Minute*10),

		pendingPeers: heap.New[peerWithPriority](),

		// will use about 1mb per torrent, can be optimized later
		pieceInfo: buildPieceInfos(info),

		private: info.Private,

		bm: bm.New(info.NumPieces),

		downloadDir: r.BasePath,
	}

	d.stateCond = gsync.NewCond(&d.m)

	d.setAnnounceList(r.Trackers)

	d.log.Info().Msg("download created")

	return d, nil
}
