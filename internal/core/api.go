// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-chi/chi/v5"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"go.uber.org/multierr"

	"neptune/internal/core/tracker"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/gslice"
)

const colAddress = "address"

var (
	errTrackerURLMissingHost = errors.New("tracker url must have a host")
	errTrackerURLBadScheme   = errors.New("only http/https tracker urls are supported")
)

type MainDataTorrent struct {
	Custom               map[string]string `json:"custom"`
	InfoHash             string            `json:"hash"`
	Name                 string            `json:"name"`
	State                string            `json:"state"`
	Comment              string            `json:"comment"`
	DirectoryBase        string            `json:"directory_base"`
	Message              string            `json:"message"`
	TrackerErrors        map[string]string `json:"tracker_errors"`
	Tags                 []string          `json:"tags"`
	DownloadRate         int64             `json:"download_rate"`
	DownloadTotal        int64             `json:"download_total"`
	UploadRate           int64             `json:"upload_rate"`
	UploadTotal          int64             `json:"upload_total"`
	ConnectionCount      int               `json:"connection_count"`
	Completed            int64             `json:"completed"`
	TotalLength          int64             `json:"total_length"`
	SelectedSize         int64             `json:"selected_size"`
	AddedAt              int64             `json:"add_at"`
	CompletedAt          int64             `json:"completed_at"`
	Private              bool              `json:"private"`
	TotalSeeding         int               `json:"total_seeding"`
	TotalDownloading     int               `json:"total_downloading"`
	ConnectedSeeding     int               `json:"connected_seeding"`
	ConnectedDownloading int               `json:"connected_downloading"`
}

type TorrentList struct {
	Torrents []MainDataTorrent `json:"torrents"`
}

func (c *Client) GetTorrentList(keys []string) TorrentList {
	c.m.RLock()
	defer c.m.RUnlock()

	torrents := make([]MainDataTorrent, len(c.downloadMap))

	for i, d := range c.downloads {
		d.m.RLock()

		msg := d.ErrorMsg()
		peers := d.peers.Size()

		custom := d.custom
		if len(keys) > 0 && custom != nil {
			custom = make(map[string]string, len(keys))
			for _, k := range keys {
				if v, ok := d.custom[k]; ok {
					custom[k] = v
				}
			}
		}

		totalSeeding, totalDownloading := d.trackerTotals()
		connectedSeeding, connectedDownloading := d.peerSeedLeecherCounts()

		torrents[i] = MainDataTorrent{
			InfoHash:             d.info.Hash.Hex(),
			Name:                 d.info.Name,
			State:                State(d.state.Load()).String(),
			DownloadRate:         d.pieceDownloadRate.Status().CurRate,
			DownloadTotal:        d.downloaded.Load(),
			UploadRate:           d.pieceUploadRate.Status().CurRate,
			UploadTotal:          d.uploaded.Load(),
			Completed:            d.completed.Load(),
			TotalLength:          d.info.TotalLength,
			SelectedSize:         d.SelectedSize(),
			Comment:              d.info.Comment,
			AddedAt:              d.AddAt,
			CompletedAt:          d.CompletedAt.Load(),
			DirectoryBase:        d.downloadDir,
			Private:              d.info.Private,
			Tags:                 d.tags,
			Custom:               custom,
			ConnectionCount:      peers,
			Message:              msg,
			TrackerErrors:        d.trackerErrors(),
			TotalSeeding:         totalSeeding,
			TotalDownloading:     totalDownloading,
			ConnectedSeeding:     connectedSeeding,
			ConnectedDownloading: connectedDownloading,
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
	down := c.pieceDownloadRate.Status()
	up := c.pieceUploadRate.Status()

	return TransferSummary{
		DownloadRate:  down.CurRate,
		DownloadTotal: down.Total,
		UploadRate:    up.CurRate,
		UploadTotal:   up.Total,
	}
}

func (c *Client) AddTorrent(raw []byte, m *metainfo.MetaInfo, info meta.Info, downloadPath string, tags []string, custom map[string]string, selectedFiles []int, skipHashCheck bool) error {
	log.Info().Msgf("try add torrent %s", info.Hash)

	if err := validateTorrentPaths(downloadPath, info); err != nil {
		return errgo.Wrap(err, "invalid torrent file paths")
	}

	c.m.RLock()
	if _, ok := c.downloadMap[info.Hash]; ok {
		c.m.RUnlock()
		return fmt.Errorf("torrent %s exists", info.Hash)
	}
	c.m.RUnlock()

	h := info.Hash.Hex()

	dir := filepath.Join(c.torrentPath, h[:2], h[2:4])
	err := os.MkdirAll(dir, os.ModePerm)
	if err != nil {
		return errgo.Wrap(err, fmt.Sprintf("failed to create director %q", dir))
	}

	err = os.WriteFile(filepath.Join(dir, h+".torrent"), raw, 0644)
	if err != nil {
		return errgo.Wrap(err, "failed to save torrent to disk")
	}

	c.m.Lock()
	defer c.m.Unlock()

	d := c.NewDownload(m, info, downloadPath, tags, custom, selectedFiles)

	c.downloads = append(c.downloads, d)

	slices.SortFunc(c.downloads, func(a, b *Download) int {
		return bytes.Compare(a.info.Hash[:], b.info.Hash[:])
	})

	c.downloadMap[info.Hash] = d
	c.infoHashes = lo.Keys(c.downloadMap)

	go d.Init(false, skipHashCheck)

	return nil
}

// validateTorrentPaths checks that all file paths in the torrent
// are safe and don't escape the download directory.
func validateTorrentPaths(basePath string, info meta.Info) error {
	base := filepath.Clean(basePath)
	for _, f := range info.Files {
		p := filepath.Clean(f.Path)
		if p == "." || p == "" {
			return fmt.Errorf("invalid torrent file path: %q", f.Path)
		}
		if filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
			return fmt.Errorf("torrent file path escapes base: %q", f.Path)
		}
		full := filepath.Clean(filepath.Join(base, p))
		if !strings.HasPrefix(full, base+string(filepath.Separator)) && full != base {
			return fmt.Errorf("torrent file path escapes base: %q", f.Path)
		}
	}
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

type APIPeers struct {
	Address      string  `json:"address"`
	Client       string  `json:"client"`
	Progress     float64 `json:"progress"`
	DownloadRate int64   `json:"download_rate"`
	UploadRate   int64   `json:"upload_rate"`
	IsIncoming   bool    `json:"is_incoming"`
}

func (c *Client) GetTorrentPeers(h metainfo.Hash) []APIPeers {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	if !ok {
		c.m.RUnlock()
		return nil
	}
	c.m.RUnlock()

	var results = make([]APIPeers, 0, d.peers.Size())

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		results = append(results, APIPeers{
			Address:      addr.String(),
			Client:       lo.FromPtrOr(p.UserAgent.Load(), ""),
			Progress:     float64(p.Bitmap.Count()) / float64(d.info.NumPieces),
			DownloadRate: p.pieceDownloadRate.Status().CurRate,
			UploadRate:   p.pieceUploadRate.Status().CurRate,
			IsIncoming:   p.Incoming,
		})
		return true
	})

	return results
}

type APITrackers struct {
	URL     string `json:"url"`
	Message string `json:"message"`
	Tier    int    `json:"tier"`
}

func (c *Client) GetTorrentTrackers(h metainfo.Hash) []APITrackers {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	if !ok {
		c.m.RUnlock()
		return nil
	}
	c.m.RUnlock()

	infos := d.Trk.List()
	results := make([]APITrackers, len(infos))
	for i, info := range infos {
		results[i] = APITrackers{
			Tier:    info.Tier,
			URL:     info.URL,
			Message: info.Err,
		}
	}
	return results
}

func (c *Client) AddTracker(h metainfo.Hash, trackerURL string, tier int) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	u, err := url.Parse(trackerURL)
	if err != nil {
		return fmt.Errorf("invalid tracker url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", errTrackerURLBadScheme, u.Scheme)
	}
	if u.Host == "" {
		return errTrackerURLMissingHost
	}

	d.Trk.Add(trackerURL, tier)
	return nil
}

func (c *Client) RemoveTracker(h metainfo.Hash, trackerURL string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.Trk.Remove(trackerURL)
	return nil
}

func (c *Client) ReplaceTrackers(h metainfo.Hash, replacements map[string]string) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	d.Trk.Replace(replacements)
	return nil
}

func (c *Client) RemoveTorrent(h metainfo.Hash, removeData bool) error {
	c.m.Lock()

	d, ok := c.downloadMap[h]
	if !ok {
		c.m.Unlock()
		return fmt.Errorf("torrent %s not exists", h)
	}

	delete(c.downloadMap, h)
	c.downloads = gslice.Remove(c.downloads, d)
	c.m.Unlock()

	d.log.Info().Msgf("torrent %s removed", h)
	d.saveResume()

	d.cancel()

	d.backgroundWg.Wait()

	d.peers.Range(func(key netip.AddrPort, p *Peer) bool {
		p.close()
		return true
	})

	dir, file := d.resumeFilePath()

	err := os.Remove(file)
	err = multierr.Append(err, pruneEmptyDirectories(dir))
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
 		fmt.Fprintf(w, "download %9s (net %9s)      upload %9s\n\n",
			humanize.IBytes(uint64(d.pieceDownloadRate.Status().CurRate))+"/s",
			humanize.IBytes(uint64(d.ioDownloadRate.Status().CurRate))+"/s",
			humanize.IBytes(uint64(d.pieceUploadRate.Status().CurRate))+"/s",
		)

		fmt.Fprintf(w, "progress: %6.2f %%\n", float64(d.completed.Load())/float64(d.SelectedSize())*100)

		fmt.Fprintf(w, "downloaded: %s  completed: %s  waste: %s\n",
			humanize.IBytes(uint64(d.downloaded.Load())),
			humanize.IBytes(uint64(d.completed.Load())),
			humanize.IBytes(uint64(d.downloaded.Load()-d.completed.Load())),
		)
		fmt.Fprintf(w, "corrupted: %s (pieces)  corrupted_bytes: %s\n",
			humanize.IBytes(uint64(d.corrupted.Load())),
			humanize.IBytes(uint64(d.corruptedBytes.Load())),
		)

		debugPrintTrackers(w, d)
		debugPrintPeers(w, d)

		if r.URL.Query().Get("mode") == "full" {
			_, _ = fmt.Fprintln(w, d.bm.String())

			_, _ = fmt.Fprintln(w, "\nmissing pieces")

			missing := bm.New(d.info.NumPieces)

			missing.Fill()

			_, _ = fmt.Fprintln(w, missing.WithAndNot(d.bm).String())
		}

		debugPrintPendingPeers(w, d)
	})
	return router
}

func debugPrintTrackers(w io.Writer, d *Download) {
	t := table.NewWriter()

	t.AppendHeader(table.Row{"tier", "url", "seeders", "leechers", "last announce", "next announce",
		"pendingPeers", "msg", "error"})

	t.SortBy([]table.SortBy{{Name: "tier"}, {Name: "url"}})

	d.Trk.Each(func(tierIdx int, tr *tracker.Tracker) {
		trackerSeed, _ := d.Trk.Seeds.Load(tr.URL)
		trackerLeecher, _ := d.Trk.Leechers.Load(tr.URL)
		t.AppendRow(table.Row{
			tierIdx,
			lo.Ellipsis(tr.URL, 40),
			trackerSeed,
			trackerLeecher,
			tr.LastAnnounceTime.Format(time.RFC3339),
			tr.NextAnnounce.Format(time.RFC3339),
			tr.PeerCount,
			tr.FailureMessage,
			tr.Err,
		})
	})

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}

func debugPrintPeers(w io.Writer, d *Download) {
	t := table.NewWriter()

	t.AppendHeader(table.Row{colAddress, "down rate", "up rate", "our req",
		"queue piece", "client", "progress",
		"peer choke", "peer interest", "our choke", "our interest", "fast", "peer req", "peer id"})

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		t.AppendRow(table.Row{
			lo.Ellipsis(addr.String(), 20),
			humanize.IBytes(uint64(p.pieceDownloadRate.Status().CurRate)) + "/s",
			humanize.IBytes(uint64(p.pieceUploadRate.Status().CurRate)) + "/s",
			p.myRequests.Size(),
			len(p.ourPieceRequests),
			*p.UserAgent.Load(),
			fmt.Sprintf("%6.1f %%", float64(p.Bitmap.Count())/float64(d.info.NumPieces)*100),
			p.peerChoking.Load(),
			p.peerInterested.Load(),
			p.ourChoking.Load(),
			p.ourInterested.Load(),
			p.allowFast.ToArray(),
			p.peerRequests.Size(),
			url.QueryEscape(p.peerID.Load().AsString()),
		})

		return true
	})

	t.SortBy([]table.SortBy{{Name: colAddress}})

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}

func debugPrintPendingPeers(w io.Writer, d *Download) {
	d.pendingPeersMutex.Lock()
	defer d.pendingPeersMutex.Unlock()

	t := table.NewWriter()
	t.AppendHeader(table.Row{colAddress})
	t.SortBy([]table.SortBy{{Name: colAddress}})

	for _, item := range d.pendingPeers.Data {
		t.AppendRow(table.Row{item.addrPort.String()})
	}

	_, _ = io.WriteString(w, t.Render())
	_, _ = fmt.Fprintln(w)
}
