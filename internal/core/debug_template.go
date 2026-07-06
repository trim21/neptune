// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"

	"neptune/internal/core/tracker"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/flowrate"
)

//go:embed debug.html
var debugTemplateFS embed.FS

var debugTmpl = template.Must(template.ParseFS(debugTemplateFS, "debug.html"))

type debugPageData struct {
	InfoHash      string
	Name          string
	DownloadRate  string
	NetRate       string
	UploadRate    string
	Progress      string
	Downloaded    string
	Completed     string
	Waste         string
	Corrupted     string
	FailingPieces []debugFailingPiece
	Trackers      []debugTracker
	Peers         []debugPeer
	PickerText    string
	Files         []debugFile
	PieceRanges   []debugPieceRange
	PendingPeers  []string
	FullMode      bool
}

type debugFailingPiece struct {
	Index     uint32
	Count     int
	BlockedBy int
}

type debugTracker struct {
	URL          string
	LastAnnounce string
	Scheduled    string
	Earliest     string
	Message      string
	Error        string
	Tier         int
	Seeders      int
	Leechers     int
	PeerCount    int
}

type debugPeer struct {
	Address      string
	DownRate     string
	UpRate       string
	Client       string
	Progress     string
	Fast         string
	PeerID       string
	OurReq       int
	ReqQ         int
	PeerReq      int
	PeerChoke    bool
	PeerInterest bool
	OurChoke     bool
	OurInterest  bool
}

type debugFile struct {
	Size     string
	Progress string
	Selected string
	Path     string
	Index    int
}

type debugPieceRange struct {
	Text string
}

func renderDebugPage(w io.Writer, data *debugPageData) error {
	return debugTmpl.Execute(w, data)
}

func buildDebugPageData(d *Download, infoHashHex string, fullMode bool) *debugPageData {
	data := &debugPageData{
		InfoHash:     infoHashHex,
		Name:         d.info.Name,
		DownloadRate: humanizeHumanReadable(d.pieceDownloadRate),
		NetRate:      humanizeHumanReadable(d.ioDownloadRate),
		UploadRate:   humanizeHumanReadable(d.pieceUploadRate),
		Progress:     fmt.Sprintf("%.2f%%", float64(d.completed.Load())/float64(d.SelectedSize())*100),
		Downloaded:   humanize.IBytes(uint64(d.downloaded.Load())),
		Completed:    humanize.IBytes(uint64(d.completed.Load())),
		Waste:        humanize.IBytes(uint64(d.downloaded.Load() - d.completed.Load())),
		Corrupted:    humanize.IBytes(uint64(d.corrupted.Load())),
		FullMode:     fullMode,
	}

	// Failing pieces
	d.corruptedPiecesMu.Lock()
	if len(d.corruptedPieces) > 0 {
		type kv struct {
			idx       uint32
			count     int
			blockedBy int
		}
		top := make([]kv, 0, len(d.corruptedPieces))
		for idx, count := range d.corruptedPieces {
			blockedBy := d.picker.countBusyBlocks(idx, d.info)
			top = append(top, kv{idx, count, blockedBy})
		}
		slices.SortFunc(top, func(a, b kv) int { return b.count - a.count })
		limit := min(len(top), 10)
		data.FailingPieces = make([]debugFailingPiece, limit)
		for i := range limit {
			data.FailingPieces[i] = debugFailingPiece{
				Index:     top[i].idx,
				Count:     top[i].count,
				BlockedBy: top[i].blockedBy,
			}
		}
	}
	d.corruptedPiecesMu.Unlock()

	// Trackers
	var trackers []debugTracker
	d.Trk.Each(func(tierIdx int, tr *tracker.Tracker) {
		trackerSeed, _ := d.Trk.Seeds.Load(tr.URL)
		trackerLeecher, _ := d.Trk.Leechers.Load(tr.URL)
		trackers = append(trackers, debugTracker{
			Tier:         tierIdx,
			URL:          tr.URL,
			Seeders:      trackerSeed,
			Leechers:     trackerLeecher,
			LastAnnounce: tr.LastAnnounceTime.Format(time.RFC3339),
			Scheduled:    tr.NextAnnounce.Format(time.RFC3339),
			Earliest:     tr.EarliestAnnounce.Format(time.RFC3339),
			PeerCount:    tr.PeerCount,
			Message:      tr.FailureMessage,
			Error:        tr.ErrorMessage(),
		})
	})
	s := &sortableTrackers{items: trackers}
	s.sort()
	data.Trackers = s.items

	// Peers
	var peers []debugPeer
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		peers = append(peers, debugPeer{
			Address:      addr.String(),
			DownRate:     humanize.IBytes(uint64(p.pieceDownloadRate.Status().CurRate)) + "/s",
			UpRate:       humanize.IBytes(uint64(p.pieceUploadRate.Status().CurRate)) + "/s",
			OurReq:       p.myRequests.Size(),
			ReqQ:         len(p.requestQueue),
			Client:       *p.UserAgent.Load(),
			Progress:     fmt.Sprintf("%.1f%%", float64(p.Bitmap.Count())/float64(d.info.NumPieces)*100),
			PeerChoke:    p.peerChoking.Load(),
			PeerInterest: p.peerInterested.Load(),
			OurChoke:     p.ourChoking.Load(),
			OurInterest:  p.ourInterested.Load(),
			Fast:         fmt.Sprint(p.allowFast.ToArray()),
			PeerReq:      p.peerRequests.Size(),
			PeerID:       url.QueryEscape(p.peerID.Load().AsString()),
		})
		return true
	})
	sp := &sortablePeers{items: peers}
	sp.sort()
	data.Peers = sp.items

	// Peer rate & total vs download
	var peerTotalCurRate int64
	var peerTotalBytes int64
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		s := p.pieceDownloadRate.Status()
		peerTotalCurRate += s.CurRate
		peerTotalBytes += s.Total
		return true
	})
	dlRate := d.pieceDownloadRate.Status().CurRate
	dlTotal := d.pieceDownloadRate.Status().Total

	// Picker stats
	st := d.picker.DebugStats(d.info)
	totalBlocks := st.FreeBlocks + st.RequestedBlocks + st.WritingBlocks + st.FinishedBlocks
	data.PickerText = fmt.Sprintf(
		"picker: %d open pieces, %d downloading pieces\n"+
			"blocks: %d free, %d requested, %d writing, %d finished (total %d)\n"+
			"downloadQueue: %d\n"+
			"rate: dl=%s/s peer_sum=%s/s (ratio %.2f)\n"+
			"total: dl=%s peer_sum=%s",
		st.OpenPieces, st.Downloading,
		st.FreeBlocks, st.RequestedBlocks, st.WritingBlocks, st.FinishedBlocks, totalBlocks,
		st.DownloadQueue,
		humanize.IBytes(uint64(dlRate)), humanize.IBytes(uint64(peerTotalCurRate)),
		float64(peerTotalCurRate)/float64(max(dlRate, 1)),
		humanize.IBytes(uint64(dlTotal)), humanize.IBytes(uint64(peerTotalBytes)),
	)

	// Files (full mode)
	if fullMode {
		files := make([]debugFile, 0, len(d.info.Files))
		var offset int64
		for i, file := range d.info.Files {
			selected := "yes"
			if d.selectedFilesSet != nil {
				if _, ok := d.selectedFilesSet[i]; !ok {
					selected = "no"
				}
			}

			startPiece := as.Uint32(offset / d.info.PieceLength)
			endPiece := min(as.Uint32((offset+file.Length+d.info.PieceLength-1)/d.info.PieceLength), d.info.NumPieces)

			var doneCount uint32
			for pi := startPiece; pi < endPiece; pi++ {
				if d.bm.Contains(pi) {
					doneCount++
				}
			}
			totalPieces := endPiece - startPiece
			progress := 0.0
			if totalPieces > 0 {
				progress = float64(doneCount) / float64(totalPieces) * 100
			}

			files = append(files, debugFile{
				Index:    i,
				Size:     humanize.IBytes(uint64(file.Length)),
				Progress: fmt.Sprintf("%.1f%%", progress),
				Selected: selected,
				Path:     filepath.Join(file.RawPath...),
			})

			offset += file.Length
		}
		data.Files = files

		// Piece ranges
		var buf strings.Builder
		writePieceRanges(&buf, "have", d.bm)
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})

		buf.Reset()
		writePieceRanges(&buf, "wanted", d.selectedPiecesBm)
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})

		missing := bm.New(d.info.NumPieces)
		missing.Fill()
		buf.Reset()
		writePieceRanges(&buf, "missing", missing.WithAndNot(d.bm).WithAnd(d.selectedPiecesBm))
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})
	}

	// Pending peers
	d.peerList.mu.Lock()
	for _, pp := range d.peerList.peers {
		if pp.connection == nil {
			data.PendingPeers = append(data.PendingPeers, pp.addrPort.String())
		}
	}
	d.peerList.mu.Unlock()

	return data
}

func humanizeHumanReadable(m *flowrate.Monitor) string {
	return humanize.IBytes(uint64(m.Status().CurRate)) + "/s"
}

// sortableTrackers wraps tracker data for sorting by tier then URL.
type sortableTrackers struct {
	items []debugTracker
}

func (s *sortableTrackers) sort() {
	slices.SortFunc(s.items, func(a, b debugTracker) int {
		if a.Tier != b.Tier {
			return a.Tier - b.Tier
		}
		if a.URL < b.URL {
			return -1
		}
		if a.URL > b.URL {
			return 1
		}
		return 0
	})
}

// sortablePeers wraps peer data for sorting by address.
type sortablePeers struct {
	items []debugPeer
	mu    sync.Mutex
}

func (s *sortablePeers) sort() {
	slices.SortFunc(s.items, func(a, b debugPeer) int {
		if a.Address < b.Address {
			return -1
		}
		if a.Address > b.Address {
			return 1
		}
		return 0
	})
}
