// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/samber/lo"

	"neptune/internal/client/tracker"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/flowrate"
)

//go:embed debug.html
var debugTemplateFS embed.FS

var debugTmpl = template.Must(template.ParseFS(debugTemplateFS, "debug.html"))

type debugPageData struct {
	UploadRate        string                  `json:"upload_rate"`
	PickerText        string                  `json:"picker_text"`
	DownloadRate      string                  `json:"download_rate"`
	NetRate           string                  `json:"net_rate"`
	InfoHash          string                  `json:"info_hash"`
	Progress          string                  `json:"progress"`
	TotalSize         string                  `json:"total_size"`
	SelectedSize      string                  `json:"selected_size"`
	Downloaded        string                  `json:"downloaded"`
	ErrorMsg          string                  `json:"error_msg"`
	Extra             string                  `json:"extra"`
	WasteStale        string                  `json:"waste_stale"`
	Name              string                  `json:"name"`
	WasteDupe         string                  `json:"waste_dupe"`
	Completed         string                  `json:"completed"`
	Corrupted         string                  `json:"corrupted"`
	LastDataAt        string                  `json:"last_data_at"`
	State             string                  `json:"state"`
	Pending           string                  `json:"pending"`
	DownloadingPieces []debugDownloadingPiece `json:"downloading_pieces,omitempty"`
	FailingPieces     []debugFailingPiece     `json:"failing_pieces,omitempty"`
	Trackers          []debugTracker          `json:"trackers,omitempty"`
	Peers             []debugPeer             `json:"peers,omitempty"`
	Files             []debugFile             `json:"files,omitempty"`
	PieceRanges       []debugPieceRange       `json:"piece_ranges,omitempty"`
	PendingPeers      []debugPendingPeer      `json:"pending_peers,omitempty"`
	NumCandidates     int                     `json:"num_connect_candidates"`
	RemainingPieces   uint32                  `json:"remaining_pieces"`
	CompletedPieces   uint32                  `json:"completed_pieces"`
	TotalPieces       uint32                  `json:"total_pieces"`
	FullMode          bool                    `json:"full_mode"`
}

type debugPendingPeer struct {
	Address     string `json:"address"`
	LastSeen    string `json:"last_seen"`
	LastError   string `json:"last_error"`
	Failcount   uint8  `json:"failcount"`
	HadTrans    bool   `json:"had_trans"`
	Connectable bool   `json:"connectable"`
}

type debugFailingPiece struct {
	Index     uint32 `json:"index"`
	Count     int    `json:"count"`
	BlockedBy int    `json:"blocked_by"`
}

type debugDownloadingPiece struct {
	Blocks     int    `json:"blocks"`
	Responded  int    `json:"responded"`
	Requested  int    `json:"requested"`
	Free       int    `json:"free"`
	Index      uint32 `json:"index"`
	HashPassed bool   `json:"hash_passed"`
	Locked     bool   `json:"locked"`
}

type debugTracker struct {
	URL          string `json:"url"`
	LastAnnounce string `json:"last_announce"`
	Scheduled    string `json:"scheduled"`
	Earliest     string `json:"earliest"`
	Message      string `json:"message"`
	Error        string `json:"error"`
	Tier         int    `json:"tier"`
	Seeders      int    `json:"seeders"`
	Leechers     int    `json:"leechers"`
	PeerCount    int    `json:"peer_count"`
}

type debugPeer struct {
	Address      string `json:"address"`
	DownRate     string `json:"down_rate"`
	UpRate       string `json:"up_rate"`
	Client       string `json:"client"`
	Progress     string `json:"progress"`
	Fast         string `json:"fast"`
	PeerID       string `json:"peer_id"`
	LastPick     string `json:"last_pick"`
	Direction    string `json:"direction"`
	Encryption   string `json:"encryption"`
	OurReq       int    `json:"our_req"`
	ReqQ         int    `json:"req_q"`
	DesiredQ     int    `json:"desired_q"`
	PeerReq      int    `json:"peer_req"`
	Snubbed      bool   `json:"snubbed"`
	PeerChoke    bool   `json:"peer_choke"`
	PeerInterest bool   `json:"peer_interest"`
	OurChoke     bool   `json:"our_choke"`
	OurInterest  bool   `json:"our_interest"`
	OnParole     bool   `json:"on_parole"`
	BlockedCount int    `json:"blocked_count"`
	TrustPoints  int32  `json:"trust_points"`
}

type debugFile struct {
	Size     string `json:"size"`
	Progress string `json:"progress"`
	Selected string `json:"selected"`
	Path     string `json:"path"`
	Index    int    `json:"index"`
}

type debugPieceRange struct {
	Text string `json:"text"`
}

func RenderDebugPage(w io.Writer, data *debugPageData) error {
	return debugTmpl.Execute(w, data)
}

func BuildDebugPageData(d *Download, infoHashHex string, fullMode bool) *debugPageData {
	data := &debugPageData{
		InfoHash:        infoHashHex,
		Name:            d.info.Name,
		State:           d.GetState().String(),
		DownloadRate:    humanizeHumanReadable(d.pieceDownloadRate),
		NetRate:         humanizeHumanReadable(d.ioDownloadRate),
		UploadRate:      humanizeHumanReadable(d.pieceUploadRate),
		Progress:        fmt.Sprintf("%.2f%%", float64(d.completed.Load())/float64(d.SelectedSize())*100),
		TotalSize:       humanize.IBytes(uint64(d.info.TotalLength)),
		SelectedSize:    humanize.IBytes(uint64(d.SelectedSize())),
		Downloaded:      humanize.IBytes(uint64(d.downloaded.Load())),
		Completed:       humanize.IBytes(uint64(d.completed.Load())),
		Extra:           humanize.IBytes(uint64(d.downloaded.Load() - d.completed.Load())),
		WasteStale:      humanize.IBytes(uint64(d.wastedStale.Load())),
		WasteDupe:       humanize.IBytes(uint64(d.wastedDupe.Load())),
		Corrupted:       humanize.IBytes(uint64(d.corrupted.Load())),
		ErrorMsg:        d.ErrorMsg(),
		LastDataAt:      lastDataAt(d.pieceDownloadRate),
		NumCandidates:   d.peerList.numCandidates(),
		TotalPieces:     d.info.NumPieces,
		CompletedPieces: d.completedBm.Count(),
		RemainingPieces: d.info.NumPieces - d.completedBm.Count(),
		FullMode:        fullMode,
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
			blockedBy := d.picker.Load().CountBusyBlocks(idx)
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
	d.tracker.Each(func(tierIdx int, tr *tracker.Tracker) {
		trackerSeed, _ := d.tracker.Seeds.Load(tr.URL)
		trackerLeecher, _ := d.tracker.Leechers.Load(tr.URL)
		trackers = append(trackers, debugTracker{
			Tier:         tierIdx,
			URL:          lo.Ellipsis(tr.URL, 60),
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
	d.peers.Range(func(_ uint64, p Peer) bool {
		dir := "out"
		if p.Incoming() {
			dir = "in"
		}
		enc := "none"
		if p.Encrypted() {
			enc = "rc4"
		}
		peers = append(peers, debugPeer{
			Address:      p.Addr().String(),
			DownRate:     humanize.IBytes(uint64(p.DownloadRate())) + "/s",
			UpRate:       humanize.IBytes(uint64(p.UploadRate())) + "/s",
			OurReq:       schedulingDebugInt(p, func(pp *peerImpl) int { return pp.OutstandingRequests() }),
			ReqQ:         schedulingDebugInt(p, func(pp *peerImpl) int { return pp.QueueLen() }),
			DesiredQ:     schedulingDebugInt(p, func(pp *peerImpl) int { return pp.DesiredQueueSize() }),
			Client:       p.UserAgent(),
			Progress:     fmt.Sprintf("%.1f%%", float64(p.PieceCount())/float64(d.info.NumPieces)*100),
			Snubbed:      p.IsSnubbed(),
			PeerChoke:    p.IsChoking(),
			PeerInterest: p.IsPeerInterested(),
			OurChoke:     p.IsOurChoking(),
			OurInterest:  p.IsOurInterested(),
			OnParole:     p.OnParole(),
			BlockedCount: p.BlockedCount(),
			TrustPoints:  p.TrustPoints(),
			Fast:         fmt.Sprint(p.FastBitmap().ToArray()),
			PeerReq:      p.PeerRequestCount(),
			PeerID:       url.QueryEscape(p.PeerIDString()),
			LastPick:     schedulingDebugStr(p, func(pp *peerImpl) string { return pp.LastPickDebug() }),
			Direction:    dir,
			Encryption:   enc,
		})
		return true
	})
	sp := &sortablePeers{items: peers}
	sp.sort()
	data.Peers = sp.items

	// Peer rate & total vs download
	var peerTotalCurRate int64
	var peerTotalBytes int64
	d.peers.Range(func(_ uint64, p Peer) bool {
		peerTotalCurRate += p.DownloadRate()
		peerTotalBytes += p.DownloadTotal()
		return true
	})
	dlRate := d.pieceDownloadRate.Status().CurRate
	dlTotal := d.pieceDownloadRate.Status().Total

	// Pending blocks: blocks received but not yet flushed to disk (piece not complete).
	data.Pending = humanize.IBytes(uint64(d.pendingBytes.Load()))

	// Picker stats
	st := d.picker.Load().DebugStats()
	totalBlocks := st.FreeBlocks + st.RequestedBlocks + st.RespondedBlocks
	data.PickerText = fmt.Sprintf(
		"picker: %d open pieces, %d downloading pieces\n"+
			"blocks: %d free, %d requested, %d responded (total %d)\n"+
			"downloadQueue: %d\n"+
			"rate: dl=%s/s peer_sum=%s/s (ratio %.2f)\n"+
			"total: dl=%s peer_sum=%s",
		st.OpenPieces, st.Downloading,
		st.FreeBlocks, st.RequestedBlocks, st.RespondedBlocks, totalBlocks,
		st.DownloadQueue,
		humanize.IBytes(uint64(dlRate)), humanize.IBytes(uint64(peerTotalCurRate)),
		float64(peerTotalCurRate)/float64(max(dlRate, 1)),
		humanize.IBytes(uint64(dlTotal)), humanize.IBytes(uint64(peerTotalBytes)),
	)

	// Downloading pieces detail
	dlPieces := d.picker.Load().DebugDownloadingPieces()
	if len(dlPieces) > 0 {
		// cap at 200 to keep the page readable
		limit := min(len(dlPieces), 200)
		data.DownloadingPieces = make([]debugDownloadingPiece, limit)
		for i := range limit {
			dp := dlPieces[i]
			data.DownloadingPieces[i] = debugDownloadingPiece(dp)
		}
	}

	// Files (full mode)
	if fullMode {
		files := make([]debugFile, 0, len(d.info.Files))
		var offset int64
		d.s.mu.RLock()
		for i, file := range d.info.Files {
			selected := "yes"
			if d.s.selectedFilesSet != nil {
				if _, ok := d.s.selectedFilesSet[i]; !ok {
					selected = "no"
				}
			}

			startPiece := as.Uint32(offset / d.info.PieceLength)
			endPiece := min(as.Uint32((offset+file.Length+d.info.PieceLength-1)/d.info.PieceLength), d.info.NumPieces)

			var doneCount uint32
			for pi := startPiece; pi < endPiece; pi++ {
				if d.completedBm.Contains(pi) {
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

		// Piece ranges — hold lock to avoid racing with buildSelectedPiecesBmUnsafe.
		var buf strings.Builder
		writePieceRanges(&buf, "have", d.completedBm)
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})

		buf.Reset()
		writePieceRanges(&buf, "wanted", d.wantedBm)
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})

		missing := bm.New(d.info.NumPieces)
		missing.Fill()
		buf.Reset()
		writePieceRanges(&buf, "missing", missing.WithAndNot(d.completedBm).WithAnd(d.wantedBm))
		data.PieceRanges = append(data.PieceRanges, debugPieceRange{Text: buf.String()})
		d.s.mu.RUnlock()
	}

	// Pending peers
	now := time.Now().Unix()
	d.peerList.mu.Lock()
	for _, pp := range d.peerList.peers {
		if pp.connection == nil {
			lastSeen := "never"
			if pp.lastSeen > 0 {
				backoff := int64(pp.failcount+1) * 60
				nextTry := pp.lastSeen + backoff - now
				lastSeen = fmt.Sprintf("%s ago (next try in %ds)",
					time.Duration(now-pp.lastSeen)*time.Second, nextTry)
			}
			data.PendingPeers = append(data.PendingPeers, debugPendingPeer{
				Address:     pp.addrPort.String(),
				Failcount:   pp.failcount,
				LastSeen:    lastSeen,
				LastError:   pp.lastErr,
				HadTrans:    pp.hadTrans,
				Connectable: pp.connectable,
			})
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
func schedulingDebugInt(p Peer, fn func(*peerImpl) int) int {
	if pp, ok := any(p).(*peerImpl); ok {
		return fn(pp)
	}
	return 0
}
func schedulingDebugStr(p Peer, fn func(*peerImpl) string) string {
	if pp, ok := any(p).(*peerImpl); ok {
		return fn(pp)
	}
	return "-"
}

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

func lastDataAt(m *flowrate.Monitor) string {
	st := m.Status()
	if st.Idle == 0 && st.Total == 0 {
		return "never"
	}
	return time.Now().Add(-st.Idle).Format(time.RFC3339)
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
	fmt.Fprintln(w)
}

// RenderDebugJSON writes the debug data as JSON.
func RenderDebugJSON(w io.Writer, data *debugPageData) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}
