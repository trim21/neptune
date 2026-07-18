// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/random"
	"neptune/internal/session"
)

// ResumeFromData constructs a Download from saved resume data.
// The returned Download has been fully data-initialized:
// - completedBm is restored from the resume bitfield and validated against disk
// - state is Stopped / Downloading / Seeding
// - runtime stats (downloaded, uploaded, etc.) are restored
// Call Init() to start background goroutines.
func ResumeFromData(sess *session.Session, data []byte) (*Download, error) {
	var r resume
	if err := bencode.Unmarshal(data, &r); err != nil {
		return nil, errgo.Wrap(err, "failed to decode resume data")
	}

	tPath := filepath.Join(sess.TorrentPath, r.InfoHash[:2], r.InfoHash[2:4], r.InfoHash+".torrent")
	m, err := metainfo.LoadFromFile(tPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errgo.Wrap(err, fmt.Sprintf("torrent %s missing at %s", r.InfoHash, tPath))
		}
		return nil, errgo.Wrap(err, "failed to decode torrent file "+tPath)
	}

	info, err := meta.FromTorrent(*m)
	if err != nil {
		return nil, errgo.Wrap(err, "failed to decode torrent data")
	}

	// Restore persisted file paths to survive truncation algorithm changes.
	if len(r.FilePaths) == len(info.Files) {
		meta.RestoreFilePaths(info.Files, r.FilePaths)
	}

	if r.SelectedFiles == nil {
		r.SelectedFiles = make([]int, len(info.Files))
		for i := range info.Files {
			r.SelectedFiles[i] = i
		}
	}

	// Build completedBm from resume bitfield, then validate against disk.
	completedBm := bm.FromBitfields(r.Bitfield, info.NumPieces)

	// Build selected bitmap for validateResumeBitfield.
	selected := bm.New(uint32(len(info.Files)))
	selected.Fill()
	if len(r.SelectedFiles) > 0 && len(r.SelectedFiles) < len(info.Files) {
		selected.Clear()
		for _, idx := range r.SelectedFiles {
			selected.Set(uint32(idx))
		}
	}

	invalidBytes, err := validateResumeBitfield(info, r.BasePath, selected, completedBm)
	if err != nil {
		return nil, err
	}

	// Compute completed bytes from the validated bitmap.
	wantedBm := buildWantedBm(info, selected)
	completed := computeCompleted(info, completedBm, selected)

	// Determine initial state.
	initState := Downloading
	resumeStopped := r.State == ResumeStopped
	if completedBm.WithAnd(wantedBm).Count() == wantedBm.Count() {
		initState = Seeding
	}

	initResult := DataInitResult{
		CompletedBm:  completedBm,
		Completed:    completed,
		InitialState: initState,
	}

	d := New(sess, m, info, r.BasePath, r.Tags, r.Custom, r.SelectedFiles, initResult)

	// ResumeStopped overrides to Stopped after construction.
	if resumeStopped {
		d.state.Store(uint32(Stopped))
	}

	if r.TrackerKey != "" {
		d.tracker.Key = r.TrackerKey
	} else {
		d.tracker.Key = random.URLSafeStr(16)
	}

	_ = invalidBytes // validated but not persisted to resume; log if needed

	d.AddAt = r.AddAt.Time
	d.completedAt.Store(r.CompletedAt.UnixNano())

	d.queueWeight.Store(r.QueueWeight)

	d.downloaded.Store(r.Downloaded)
	d.downloadAtStart = r.Downloaded
	d.uploaded.Store(r.Uploaded)
	d.corrupted.Store(r.Corrupted)
	d.uploadAtStart = r.Uploaded
	d.downloadLimiter.Update(r.DownloadSpeedLimit)
	d.uploadLimiter.Update(r.UploadSpeedLimit)

	// Restore piece pick strategy from resume.
	s := r.PiecePickStrategy
	if s > 1 {
		s = 0
	}
	d.piecePickStrategy.Store(s)

	return d, nil
}

// TrkStagger calls Stagger on the download's tracker set.
func (d *Download) TrkStagger(maxDelay time.Duration) {
	d.tracker.Stagger(maxDelay)
}
