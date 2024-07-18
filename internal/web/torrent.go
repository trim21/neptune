// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package web

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/docker/go-units"
	"github.com/dustin/go-humanize"
	"github.com/swaggest/usecase"
	"github.com/trim21/errgo"

	"tyr/internal/core"
	"tyr/internal/meta"
	"tyr/internal/metainfo"
	"tyr/internal/web/jsonrpc"
)

type AddTorrentRequest struct {
	TorrentFile []byte   `json:"torrent_file" required:"true" description:"base64 encoded torrent file content" validate:"required"`
	DownloadDir string   `json:"download_dir" description:"download dir"`
	Tags        []string `json:"tags"`
	IsBaseDir   bool     `json:"is_base_dir" description:"if true, will not append torrent name to download_dir"`
}

type AddTorrentResponse struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

func AddTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*AddTorrentRequest, AddTorrentResponse](
		func(ctx context.Context, req *AddTorrentRequest, res *AddTorrentResponse) error {
			m, err := metainfo.Load(bytes.NewBuffer(req.TorrentFile))
			if err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to parse torrent file"))
			}

			info, err := meta.FromTorrent(*m)
			if err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to parse torrent info"))
			}

			if info.PieceLength > 256*units.MiB {
				return CodeError(4,
					fmt.Errorf("piece length %s too big, only allow <= 256 MiB",
						humanize.IBytes(uint64(info.PieceLength))))
			}

			var downloadDir = req.DownloadDir

			if downloadDir == "" {
				downloadDir = c.Config.App.DownloadDir
			} else {
				if !req.IsBaseDir {
					downloadDir = filepath.Join(req.DownloadDir, info.Name)
				}
			}

			if req.Tags == nil {
				req.Tags = []string{}
			}
			err = c.AddTorrent(m, info, downloadDir, req.Tags)
			if err != nil {
				return CodeError(5, errgo.Wrap(err, "failed to add torrent to download"))
			}

			res.InfoHash = info.Hash.Hex()

			return nil
		},
	)
	u.SetName("torrent.add")
	h.Add(u)
}

type GetTorrentRequest struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

type GetTorrentResponse struct {
	Name string   `json:"name" required:"true"`
	Tags []string `json:"tags"`
}

func GetTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*GetTorrentRequest, GetTorrentResponse](
		func(ctx context.Context, req *GetTorrentRequest, res *GetTorrentResponse) error {
			r, err := hex.DecodeString(req.InfoHash)
			if err != nil || len(r) != 20 {
				return CodeError(1, errgo.Wrap(err, "invalid info_hash"))
			}

			info, err := c.GetTorrent(metainfo.Hash(r))

			if err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to get download"))
			}

			res.Name = info.Name

			if info.Tags == nil {
				res.Tags = []string{}
			} else {
				res.Tags = info.Tags
			}

			return nil
		},
	)

	u.SetName("torrent.get")
	h.Add(u)
}

type MoveTorrentRequest struct {
	InfoHash       string `json:"info_hash" description:"torrent file hash" required:"true"`
	TargetBasePath string `json:"target_base_path" required:"true"`
}

type MoveTorrentResponse struct {
}

func MoveTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*MoveTorrentRequest, MoveTorrentResponse](
		func(ctx context.Context, req *MoveTorrentRequest, res *MoveTorrentResponse) error {
			ih, err := hex.DecodeString(req.InfoHash)
			if err != nil || len(ih) != 20 {
				return CodeError(1, errgo.Wrap(err, "invalid info_hash"))
			}

			err = c.ScheduleMove(metainfo.Hash(ih), req.TargetBasePath)
			if err != nil {
				return CodeError(2, errgo.Wrap(err, "failed to schedule move"))
			}

			return nil
		},
	)

	u.SetName("torrent.move")
	h.Add(u)
}

type listTorrentRequest struct {
}

type listTorrentResponse struct {
	core.TorrentList
}

func listTorrent(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*listTorrentRequest, listTorrentResponse](
		func(ctx context.Context, req *listTorrentRequest, res *listTorrentResponse) error {
			res.TorrentList = c.GetTorrentList()
			return nil
		},
	)
	u.SetName("torrent.list")
	h.Add(u)
}

type getTransferSummaryRequest struct {
}

type getTransferSummaryResponse struct {
	core.TransferSummary
}

func getTransferSummary(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*getTransferSummaryRequest, getTransferSummaryResponse](
		func(ctx context.Context, req *getTransferSummaryRequest, res *getTransferSummaryResponse) error {
			res.TransferSummary = c.GetTransferSummary()
			return nil
		},
	)
	u.SetName("transfer_summary")
	h.Add(u)
}

var errInvalidInfoHash = errors.New("invalid info hash")

type listTorrentFilesRequest struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

type listTorrentFilesResponse struct {
	Files []core.TorrentFile `json:"files"`
}

func listTorrentFiles(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*listTorrentFilesRequest, listTorrentFilesResponse](
		func(ctx context.Context, req *listTorrentFilesRequest, res *listTorrentFilesResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			h, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			res.Files = c.GetTorrentFiles(metainfo.Hash(h))

			return nil
		},
	)
	u.SetName("torrent.files")
	h.Add(u)
}

type listTorrentPeersRequest struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

type listTorrentPeersResponse struct {
	Peers []core.ApiPeers `json:"peers"`
}

func listTorrentPeers(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*listTorrentPeersRequest, listTorrentPeersResponse](
		func(ctx context.Context, req *listTorrentPeersRequest, res *listTorrentPeersResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			h, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			res.Peers = c.GetTorrentPeers(metainfo.Hash(h))

			return nil
		},
	)
	u.SetName("torrent.peers")
	h.Add(u)
}

type listTorrentTrackersRequest struct {
	InfoHash string `json:"info_hash" description:"torrent file hash" required:"true"`
}

type listTorrentTrackersResponse struct {
	Trackers []core.ApiTrackers `json:"trackers"`
}

func listTorrentTrackers(h *jsonrpc.Handler, c *core.Client) {
	u := usecase.NewInteractor[*listTorrentTrackersRequest, listTorrentTrackersResponse](
		func(ctx context.Context, req *listTorrentTrackersRequest, res *listTorrentTrackersResponse) error {
			if len(req.InfoHash) != sha1.Size*2 {
				return errInvalidInfoHash
			}

			h, err := hex.DecodeString(req.InfoHash)
			if err != nil {
				return errInvalidInfoHash
			}

			res.Trackers = c.GetTorrentTrackers(metainfo.Hash(h))

			return nil
		},
	)
	u.SetName("torrent.trackers")
	h.Add(u)
}
