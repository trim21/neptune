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

	if r.SelectedFiles == nil {
		r.SelectedFiles = make([]int, len(info.Files))
		for i := range info.Files {
			r.SelectedFiles[i] = i
		}
	}

	d := New(sess, m, info, r.BasePath, r.Tags, r.Custom, r.SelectedFiles)

	d.completedBm = bm.FromBitfields(r.Bitfield, d.info.NumPieces)
	if d.completedBm.Count() == d.info.NumPieces {
		d.picker.Store(nil)
	} else {
		for i := range d.info.NumPieces {
			if d.completedBm.Contains(i) {
				d.picker.Load().weHave(i, d.info)
			}
		}
	}
	d.markUnselectedPiecesDoneUnsafe()
	d.completed.Store(d.computeCompletedUnsafe())
	d.state.Store(uint32(r.State))
	d.AddAt = r.AddAt
	d.CompletedAt.Store(r.CompletedAt)

	d.downloaded.Store(r.Downloaded)
	d.downloadAtStart = r.Downloaded
	d.uploaded.Store(r.Uploaded)
	d.corrupted.Store(r.Corrupted)
	d.uploadAtStart = r.Uploaded
	d.downloadLimiter.Update(r.DownloadSpeedLimit)
	d.uploadLimiter.Update(r.UploadSpeedLimit)

	return d, nil
}

// TrkStagger calls Stagger on the download's tracker set.
func (d *Download) TrkStagger(totalDownloads int) {
	d.Trk.Stagger(totalDownloads)
}
