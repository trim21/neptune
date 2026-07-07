// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"github.com/samber/lo"

	"neptune/internal/pkg/as"
)

// TorrentInfo is a read-only snapshot of a torrent's state returned by Download.Info().
// The caller MUST NOT retain any reference after the call returns.
type TorrentInfo struct {
	Custom               map[string]string
	TrackerErrors        map[string]string
	Name                 string
	Hash                 string
	Comment              string
	DownloadDir          string
	ErrorMessage         string
	Tags                 []string
	UploadTotal          int64
	AddedAt              int64
	DownloadTotal        int64
	UploadRate           int64
	ConnectedDownloading int
	ConnectionCount      int
	BytesCompleted       int64
	TotalLength          int64
	SelectedSize         int64
	DownloadRate         int64
	CompletedAt          int64
	ConnectedSeeding     int
	Corrupted            int64
	TotalSeeding         int
	TotalDownloading     int
	Private              bool
	State                State
}

// Info returns a snapshot of the download's state for use by external callers.
// It handles its own locking. If keys is non-empty, only those custom keys are included.
func (d *Download) Info(keys []string) TorrentInfo {
	d.s.mu.RLock()
	defer d.s.mu.RUnlock()

	custom := d.s.custom
	if len(keys) > 0 && custom != nil {
		custom = make(map[string]string, len(keys))
		for _, k := range keys {
			if v, ok := d.s.custom[k]; ok {
				custom[k] = v
			}
		}
	}

	totalSeeding, totalDownloading := d.trackerTotals()
	connectedSeeding, connectedDownloading := d.peerSeedLeecherCounts()

	return TorrentInfo{
		Hash:                 d.info.Hash.Hex(),
		Name:                 d.info.Name,
		State:                State(d.state.Load()),
		Comment:              d.info.Comment,
		DownloadDir:          d.s.downloadDir,
		ErrorMessage:         d.ErrorMsg(),
		TrackerErrors:        d.trackerErrors(),
		Tags:                 d.s.tags,
		Custom:               custom,
		DownloadRate:         d.pieceDownloadRate.Status().CurRate,
		DownloadTotal:        d.downloaded.Load(),
		UploadRate:           d.pieceUploadRate.Status().CurRate,
		UploadTotal:          d.uploaded.Load(),
		ConnectionCount:      d.peers.Size(),
		BytesCompleted:       d.completed.Load(),
		TotalLength:          d.info.TotalLength,
		SelectedSize:         d.SelectedSize(),
		AddedAt:              d.AddAt,
		CompletedAt:          d.CompletedAt.Load(),
		Private:              d.info.Private,
		Corrupted:            d.corrupted.Load(),
		TotalSeeding:         totalSeeding,
		TotalDownloading:     totalDownloading,
		ConnectedSeeding:     connectedSeeding,
		ConnectedDownloading: connectedDownloading,
	}
}

// FileInfo is a read-only snapshot of a file in a torrent.
type FileInfo struct {
	Path     []string
	Length   int64
	Progress float64
}

// Files returns file-level information for the torrent.
func (d *Download) Files() []FileInfo {
	results := make([]FileInfo, len(d.info.Files))
	var fileStart int64
	for i, file := range d.info.Files {
		fileEnd := fileStart + file.Length
		startIndex := as.Uint32(fileStart / d.info.PieceLength)
		endIndex := as.Uint32((fileEnd + d.info.PieceLength - 1) / d.info.PieceLength)
		pieceDoneCount := 0
		for pi := startIndex; pi < endIndex; pi++ {
			if d.completedBm.Contains(pi) {
				pieceDoneCount++
			}
		}
		var progress float64
		if endIndex > startIndex {
			progress = float64(pieceDoneCount) / float64(endIndex-startIndex)
		}
		results[i] = FileInfo{
			Path:     file.RawPath,
			Length:   file.Length,
			Progress: progress,
		}
		fileStart = fileEnd
	}
	return results
}

// PeerInfo is a read-only snapshot of a connected peer.
type PeerInfo struct {
	Address      string
	Client       string
	Progress     float64
	DownloadRate int64
	UploadRate   int64
	IsIncoming   bool
	Encrypted    bool
}

// PeerInfos returns a snapshot of all connected peers.
func (d *Download) PeerInfos() []PeerInfo {
	results := make([]PeerInfo, 0, d.peers.Size())
	d.peers.Range(func(_ uint64, p *Peer) bool {
		results = append(results, PeerInfo{
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
