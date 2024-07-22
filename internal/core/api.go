// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"go.uber.org/multierr"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/pkg/gslice"
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
			Completed:       d.completed.Load(),
			TotalLength:     d.info.TotalLength,
			Comment:         d.info.Comment,
			AddedAt:         d.AddAt,
			DirectoryBase:   d.downloadDir,
			Private:         d.info.Private,
			Tags:            d.tags,
			ConnectionCount: d.peers.Size(),
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
	down := c.ioDown.Status()
	up := c.ioUp.Status()

	return TransferSummary{
		DownloadRate:  down.CurRate,
		DownloadTotal: down.Total,
		UploadRate:    up.CurRate,
		UploadTotal:   up.Total,
	}
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
			if d.bm.Contains(i) {
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

	var results = make([]ApiPeers, 0, d.peers.Size())

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
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

type ApiTrackers struct {
	URL  string `json:"url"`
	Tier int    `json:"tier"`
}

func (c *Client) GetTorrentTrackers(h metainfo.Hash) []ApiTrackers {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	if !ok {
		c.m.RUnlock()
		return nil
	}
	c.m.RUnlock()

	var results = make([]ApiTrackers, 0, 10)

	d.trackerMutex.RLock()
	defer d.trackerMutex.RUnlock()

	for iterIndex, tier := range d.trackers {
		for _, tracker := range tier.trackers {
			results = append(results, ApiTrackers{
				Tier: iterIndex,
				URL:  tracker.url,
			})
		}
	}

	return results
}

func (c *Client) RemoveTorrent(h metainfo.Hash, removeData bool) error {
	c.m.Lock()
	defer c.m.Unlock()

	d, ok := c.downloadMap[h]
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.log.Info().Msg("torrent removed")

	delete(c.downloadMap, h)
	c.downloads = gslice.Remove(c.downloads, d)

	d.cancel()

	d.filePool.Cache.Purge()

	var err error
	if removeData {
		for _, f := range d.info.Files {
			e := os.Remove(filepath.Join(d.basePath, f.Path))
			if os.IsNotExist(e) {
				continue
			}

			err = multierr.Append(err, e)
		}

		if err != nil {
			err = multierr.Append(err, pruneEmptyDirectories(d.basePath))
		}
	}

	return err
}

func (c *Client) DebugHandlers() http.Handler {
	router := chi.NewRouter()
	router.Get("/{info_hash}", func(w http.ResponseWriter, r *http.Request) {
		h := r.PathValue("info_hash")
		if len(h) != 40 {
			http.Error(w, "invalid info_hash", http.StatusBadRequest)
			return
		}

		hash, err := hex.DecodeString(h)
		if err != nil {
			http.Error(w, "invalid info_hash", http.StatusBadRequest)
			return
		}

		infoHash := metainfo.Hash(hash)

		c.m.RLock()
		defer c.m.RUnlock()

		d, ok := c.downloadMap[infoHash]
		if !ok {
			http.Error(w, "download not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintf(w, "%q\n\n", d.info.Name)
		fmt.Fprintf(w, "download %9s                         upload %9s\n\n",
			humanize.IBytes(uint64(d.ioDown.Status().CurRate))+"/s",
			humanize.IBytes(uint64(d.ioUp.Status().CurRate))+"/s",
		)

		fmt.Fprintf(w, "progress: %6.2f %%\n", float64(d.completed.Load())/float64(d.info.TotalLength)*100)

		debugPrintTrackers(w, d)
		debugPrintPeers(w, d)

		_, _ = fmt.Fprintln(w, d.bm.String())

		_, _ = fmt.Fprintln(w, "\nmissing pieces")

		missing := bm.New(d.info.NumPieces)

		missing.Fill()

		_, _ = fmt.Fprintln(w, missing.WithAndNot(d.bm).String())

		debugPrintPendingPeers(w, d)
	})
	return router
}

func debugPrintTrackers(w io.Writer, d *Download) {
	d.trackerMutex.RLock()
	defer d.trackerMutex.RUnlock()

	t := table.NewWriter()

	t.AppendHeader(table.Row{"tier", "url", "seeders", "leechers", "last announce", "next announce", "pendingPeers", "msg", "error"})

	t.SortBy([]table.SortBy{{Name: "tier"}, {Name: "url"}})

	for iterIndex, tier := range d.trackers {
		for _, tracker := range tier.trackers {
			t.AppendRow(table.Row{
				iterIndex,
				lo.Elipse(tracker.url, 40),
				0,
				0,
				tracker.lastAnnounceTime.Format(time.RFC3339),
				tracker.nextAnnounce.Format(time.RFC3339),
				tracker.peerCount,
				tracker.failureMessage,
				tracker.err,
			})
		}
	}

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}

func debugPrintPeers(w io.Writer, d *Download) {
	t := table.NewWriter()

	t.AppendHeader(table.Row{"address", "download rate", "pending requests", "pending pieces", "client", "progress",
		"peer choke", "peer interested", "our choke", "our interest", "allow fast"})

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		t.AppendRow(table.Row{
			addr,
			humanize.IBytes(uint64(p.ioIn.Status().CurRate)) + "/s",
			p.myRequests.Size(),
			len(p.ourPieceRequests),
			*p.UserAgent.Load(),
			fmt.Sprintf("%6.2f %%", float64(p.Bitmap.Count())/float64(d.info.NumPieces)*100),
			p.peerChoking.Load(),
			p.peerInterested.Load(),
			p.amChoking.Load(),
			p.amInterested.Load(),
			p.allowFast.ToArray(),
		})
		return true
	})

	t.SortBy([]table.SortBy{{Name: "address"}})

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}

func debugPrintPendingPeers(w io.Writer, d *Download) {
	d.peersMutex.Lock()
	defer d.peersMutex.Unlock()

	t := table.NewWriter()
	t.AppendHeader(table.Row{"address"})
	t.SortBy([]table.SortBy{{Name: "address"}})

	for _, item := range d.pendingPeers.Data {
		t.AppendRow(table.Row{item.addrPort.String()})
	}

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}
