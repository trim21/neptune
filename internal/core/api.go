// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
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
	Corrupted            int64             `json:"corrupted"`
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
			Corrupted:            d.corrupted.Load(),
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

	d := c.NewDownload(m, info, downloadPath, tags, custom, selectedFiles)

	c.m.Lock()
	defer c.m.Unlock()

	if _, ok := c.downloadMap[info.Hash]; ok {
		return fmt.Errorf("torrent %s exists", info.Hash)
	}

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
			if d.completedBm.Contains(i) {
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
	Encrypted    bool    `json:"encrypted"`
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

	d.peers.Range(func(_ uint64, p *Peer) bool {
		results = append(results, APIPeers{
			Address:      p.Address.String(),
			Client:       lo.FromPtrOr(p.UserAgent.Load(), ""),
			Progress:     float64(p.Bitmap.Count()) / float64(d.info.NumPieces),
			DownloadRate: p.pieceDownloadRate.Status().CurRate,
			UploadRate:   p.pieceUploadRate.Status().CurRate,
			IsIncoming:   p.Incoming,
			Encrypted:    p.Encrypted,
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

func (c *Client) Reannounce(h metainfo.Hash) error {
	c.m.RLock()
	d, ok := c.downloadMap[h]
	c.m.RUnlock()
	if !ok {
		return fmt.Errorf("torrent %s not exists", h)
	}

	var event tracker.AnnounceEvent
	switch {
	case d.HasState(Downloading):
		event = tracker.EventStarted
	case d.HasState(Seeding):
		event = tracker.EventCompleted
	default:
		// Stopped, Checking, Moving, Error — send a regular update without event.
		event = ""
	}

	if !d.Trk.ForceReannounce(event) {
		return errors.New("reannounce not allowed yet: earliest interval not expired")
	}
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

	d.peers.Range(func(_ uint64, p *Peer) bool {
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

	// POST reannounce handler
	router.Post("/{info_hash}/reannounce", func(w http.ResponseWriter, r *http.Request) {
		// CSRF protection: only accept application/json.
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			http.Error(w, "invalid content type", http.StatusUnsupportedMediaType)
			return
		}

		var req struct {
			InfoHash string `json:"info_hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		if len(req.InfoHash) != 40 {
			http.Error(w, "invalid info_hash", http.StatusBadRequest)
			return
		}
		hash, err := hex.DecodeString(req.InfoHash)
		if err != nil {
			http.Error(w, "invalid info_hash", http.StatusBadRequest)
			return
		}

		if err := c.Reannounce(metainfo.Hash(hash)); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

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
		d, ok := c.downloadMap[infoHash]
		c.m.RUnlock()

		if !ok {
			http.Error(w, "download not found", http.StatusNotFound)
			return
		}

		data := buildDebugPageData(d, h, r.URL.Query().Get("mode") == "full")

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		if err := renderDebugPage(w, data); err != nil {
			log.Error().Err(err).Msg("failed to render debug page")
		}
	})
	return router
}

// writePieceRanges writes compressed piece ranges like "0-5726" instead of listing each piece.
func writePieceRanges(w io.Writer, label string, bits *bm.Bitmap) {
	count := bits.Count()
	fmt.Fprintf(w, "%s: %d pieces\n", label, count)

	if count == 0 {
		return
	}

	var (
		rangeStart uint32
		rangeEnd   uint32
		first      = true
		inRange    bool
	)

	bits.Range(func(x uint32) {
		if !inRange {
			rangeStart = x
			rangeEnd = x
			inRange = true
			return
		}
		if x == rangeEnd+1 {
			rangeEnd = x
		} else {
			if !first {
				_, _ = fmt.Fprint(w, ", ")
			}
			first = false
			if rangeStart == rangeEnd {
				fmt.Fprintf(w, "%d", rangeStart)
			} else {
				fmt.Fprintf(w, "%d-%d", rangeStart, rangeEnd)
			}
			rangeStart = x
			rangeEnd = x
		}
	})

	// flush last range
	if inRange {
		if !first {
			_, _ = fmt.Fprint(w, ", ")
		}
		if rangeStart == rangeEnd {
			fmt.Fprintf(w, "%d", rangeStart)
		} else {
			fmt.Fprintf(w, "%d-%d", rangeStart, rangeEnd)
		}
	}
	_, _ = fmt.Fprintln(w)
}
