// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/random"
	"neptune/internal/session"
)

// ResumeFromData constructs a Download from saved resume data.
// The returned Download has NOT been Init()-ed yet — the caller must call d.Init and add to Client's map.
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

	d := New(sess, m, info, r.BasePath, r.Tags, r.Custom, r.SelectedFiles)

	if r.TrackerKey != "" {
		d.tracker.Key = r.TrackerKey
	} else {
		d.tracker.Key = random.URLSafeStr(16)
	}

	d.completedBm = bm.FromBitfields(r.Bitfield, d.info.NumPieces)
	if d.completedBm.Count() == d.info.NumPieces {
		d.picker.Store(nil)
	} else {
		for i := range d.info.NumPieces {
			if d.completedBm.Contains(i) {
				d.picker.Load().WeHave(i)
			}
		}
	}
	// Restore state from resume data. ResumeStopped maps to Stopped;
	// anything else (including historical raw State values from old resume
	// files) is treated as active.
	d.markUnselectedPiecesDoneUnsafe()
	d.completed.Store(d.computeCompletedUnsafe())

	if r.State == ResumeStopped {
		d.state.Store(uint32(Stopped))
	} else if d.completedBm.Count() == d.info.NumPieces {
		d.state.Store(uint32(Seeding))
	} else {
		d.state.Store(uint32(Downloading))
	}
	d.AddAt = r.AddAt
	d.CompletedAt.Store(r.CompletedAt)

	if r.QueueWeight != 0 {
		d.queueWeight.Store(r.QueueWeight)
	} else {
		d.queueWeight.Store(-d.AddAt)
	}

	d.downloaded.Store(r.Downloaded)
	d.downloadAtStart = r.Downloaded
	d.uploaded.Store(r.Uploaded)
	d.corrupted.Store(r.Corrupted)
	d.uploadAtStart = r.Uploaded
	d.downloadLimiter.Update(r.DownloadSpeedLimit)
	d.uploadLimiter.Update(r.UploadSpeedLimit)

	// Restore piece pick strategy from resume.
	// Clamp unknown values to rarest-first.
	s := r.PiecePickStrategy
	if s > 1 {
		s = 0
	}
	d.piecePickStrategy.Store(s)

	return d, nil
}

// TrkStagger calls Stagger on the download's tracker set.
func (d *Download) TrkStagger(totalDownloads int) {
	d.tracker.Stagger(totalDownloads)
}
