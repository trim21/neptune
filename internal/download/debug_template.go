// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"embed"
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
	InfoHash          string
	Name              string
	DownloadRate      string
	NetRate           string
	UploadRate        string
	Progress          string
	TotalSize         string
	SelectedSize      string
	Downloaded        string
	Completed         string
	Waste             string
	Corrupted         string
	ErrorMsg          string
	FailingPieces     []debugFailingPiece
	Trackers          []debugTracker
	Peers             []debugPeer
	PickerText        string
	DownloadingPieces []debugDownloadingPiece
	Files             []debugFile
	PieceRanges       []debugPieceRange
	PendingPeers      []debugPendingPeer
	FullMode          bool
}

type debugPendingPeer struct {
	Address     string
	LastSeen    string
	LastError   string
	Failcount   uint8
	HadTrans    bool
	Connectable bool
}

type debugFailingPiece struct {
	Index     uint32
	Count     int
	BlockedBy int
}

type debugDownloadingPiece struct {
	Blocks     int
	Responded  int
	Requested  int
	Free       int
	Index      uint32
	HashPassed bool
	Locked     bool
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
	LastPick     string
	Direction    string
	Encryption   string
	OurReq       int
	ReqQ         int
	DesiredQ     int
	PeerReq      int
	Snubbed      bool
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

func RenderDebugPage(w io.Writer, data *debugPageData) error {
	return debugTmpl.Execute(w, data)
}

func BuildDebugPageData(d *Download, infoHashHex string, fullMode bool) *debugPageData {
	data := &debugPageData{
		InfoHash:     infoHashHex,
		Name:         d.info.Name,
		DownloadRate: humanizeHumanReadable(d.pieceDownloadRate),
		NetRate:      humanizeHumanReadable(d.ioDownloadRate),
		UploadRate:   humanizeHumanReadable(d.pieceUploadRate),
		Progress:     fmt.Sprintf("%.2f%%", float64(d.completed.Load())/float64(d.SelectedSize())*100),
		TotalSize:    humanize.IBytes(uint64(d.info.TotalLength)),
		SelectedSize: humanize.IBytes(uint64(d.SelectedSize())),
		Downloaded:   humanize.IBytes(uint64(d.downloaded.Load())),
		Completed:    humanize.IBytes(uint64(d.completed.Load())),
		Waste:        humanize.IBytes(uint64(d.downloaded.Load() - d.completed.Load())),
		Corrupted:    humanize.IBytes(uint64(d.corrupted.Load())),
		ErrorMsg:     d.ErrorMsg(),
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
			blockedBy := d.picker.Load().CountBusyBlocks(idx, d.info)
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

	// Picker stats
	st := d.picker.Load().DebugStats(d.info)
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
	dlPieces := d.picker.Load().DebugDownloadingPieces(d.info)
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
		d.s.mu.RUnlock()
		data.Files = files

		// Piece ranges
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
