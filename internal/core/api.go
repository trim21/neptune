// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"fmt"
	"net/netip"
	"slices"

	"github.com/rs/zerolog/log"
	"github.com/samber/lo"

	"tyr/internal/meta"
	"tyr/internal/metainfo"
	"tyr/internal/pkg/as"
	"tyr/internal/pkg/global/tasks"
)

type MainDataTorrent struct {
	InfoHash        string   `json:"hash"`
	Name            string   `json:"name"`
	State           string   `json:"state"`
	Comment         string   `json:"comment"`
	DirectoryBase   string   `json:"directory_base"`
	Message         string   `json:"message"`
	Tags            []string `json:"tags"`
	DownloadRate    int64    `json:"download_rate"`
	DownloadTotal   int64    `json:"download_total"`
	UploadRate      int64    `json:"upload_rate"`
	UploadTotal     int64    `json:"upload_total"`
	ConnectionCount int      `json:"connection_count"`
	Completed       int64    `json:"completed"`
	TotalLength     int64    `json:"total_length"`
	AddedAt         int64    `json:"add_at"`
	Private         bool     `json:"private"`
}

type TorrentList struct {
	Torrents []MainDataTorrent `json:"torrents"`
}

func (c *Client) GetTorrentList() TorrentList {
	c.m.RLock()
	defer c.m.RUnlock()

	torrents := make([]MainDataTorrent, len(c.downloadMap))

	for i, d := range c.downloads {
		d.m.RLock()

		msg := ""
		if d.err != nil {
			msg = d.err.Error()
		}

		torrents[i] = MainDataTorrent{
			InfoHash:        d.info.Hash.Hex(),
			Name:            d.info.Name,
			State:           d.state.String(),
			DownloadRate:    d.ioDown.Status().CurRate,
			DownloadTotal:   d.downloaded.Load(),
			UploadRate:      d.ioUp.Status().CurRate,
			UploadTotal:     d.uploaded.Load(),
			Completed:       d.completed(),
			TotalLength:     d.info.TotalLength,
			Comment:         d.info.Comment,
			AddedAt:         d.AddAt,
			DirectoryBase:   d.downloadDir,
			Private:         d.info.Private,
			Tags:            d.tags,
			ConnectionCount: d.conn.Size(),
			Message:         msg,
		}

		d.m.RUnlock()
	}

	return TorrentList{
		Torrents: torrents,
	}
}

type TransferSummary struct {
	DownloadRate  int64 `json:"download_rate"`
	DownloadTotal int64 `json:"download_total"`
	UploadRate    int64 `json:"upload_rate"`
	UploadTotal   int64 `json:"upload_total"`
}

func (c *Client) GetTransferSummary() TransferSummary {
	c.m.RLock()
	defer c.m.RUnlock()

	var result TransferSummary

	for _, d := range c.downloads {
		result.DownloadRate += d.netDown.Status().CurRate
		result.UploadRate += d.ioUp.Status().CurRate
	}

	return result
}

func (c *Client) AddTorrent(m *metainfo.MetaInfo, info meta.Info, downloadPath string, tags []string) error {
	log.Info().Msgf("try add torrent %s", info.Hash)

	c.m.RLock()
	if _, ok := c.downloadMap[info.Hash]; ok {
		c.m.RUnlock()
		return fmt.Errorf("torrent %s exists", info.Hash)
	}
	c.m.RUnlock()

	c.m.Lock()
	defer c.m.Unlock()

	d := c.NewDownload(m, info, downloadPath, tags)

	c.downloads = append(c.downloads, d)

	slices.SortFunc(c.downloads, func(a, b *Download) int {
		return bytes.Compare(a.info.Hash[:], b.info.Hash[:])
	})

	c.downloadMap[info.Hash] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	tasks.Submit(d.Init)

	return nil
}

type TorrentFile struct {
	Path     []string `json:"path"`
	Index    int      `json:"index"`
	Progress float64  `json:"progress"`
	Size     int64    `json:"size"`
}

func (c *Client) GetTorrentFiles(h metainfo.Hash) []TorrentFile {
	c.m.RLock()
	defer c.m.RUnlock()

	d, ok := c.downloadMap[h]
	if !ok {
		return nil
	}

	var results = make([]TorrentFile, len(d.info.Files))

	var fileStart int64 = 0
	var fileEnd int64

	for i, file := range d.info.Files {
		fileEnd = fileStart + file.Length

		startIndex := as.Uint32(fileStart / d.info.TotalLength)
		endIndex := as.Uint32((fileEnd + d.info.TotalLength - 1) / d.info.TotalLength)

		pieceDoneCount := 0

		for i := startIndex; i < endIndex; i++ {
			if d.bm.Get(i) {
				pieceDoneCount++
			}
		}

		results[i] = TorrentFile{
			Index:    i,
			Path:     file.RawPath,
			Progress: float64(pieceDoneCount) / float64(endIndex-startIndex),
			Size:     file.Length,
		}

		fileStart = fileStart + file.Length
	}

	return results
}

type ApiPeers struct {
	Address      string  `json:"address"`
	Client       string  `json:"client"`
	Progress     float64 `json:"progress"`
	DownloadRate int64   `json:"download_rate"`
	UploadRate   int64   `json:"upload_rate"`
	IsIncoming   bool    `json:"is_incoming"`
}

func (c *Client) GetTorrentPeers(h metainfo.Hash) []ApiPeers {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	if !ok {
		c.m.RUnlock()
		return nil
	}
	c.m.RUnlock()

	var results = make([]ApiPeers, 0, d.conn.Size())

	d.conn.Range(func(addr netip.AddrPort, p *Peer) bool {
		results = append(results, ApiPeers{
			Address:      addr.String(),
			Client:       lo.FromPtrOr(p.UserAgent.Load(), ""),
			Progress:     float64(p.Bitmap.Count()) / float64(d.info.NumPieces),
			DownloadRate: p.ioIn.Status().CurRate,
			UploadRate:   p.ioOut.Status().CurRate,
			IsIncoming:   p.Incoming,
		})
		return true
	})

	return results
}
