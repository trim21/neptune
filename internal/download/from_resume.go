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
	"neptune/internal/session"
)

// LoadFromResume validates saved state and returns a fully initialized Download.
func LoadFromResume(sess *session.Session, data []byte, trackerStagger time.Duration) (*Download, error) {
	var r resume
	if err := bencode.Unmarshal(data, &r); err != nil {
		return nil, errgo.Wrap(err, "failed to decode resume data")
	}
	if len(r.InfoHash) != 40 {
		return nil, fmt.Errorf("invalid resume info hash %q", r.InfoHash)
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
	if info.Hash.Hex() != r.InfoHash {
		return nil, fmt.Errorf("resume info hash %s does not match torrent %s", r.InfoHash, info.Hash.Hex())
	}

	// Restore persisted file paths to survive truncation algorithm changes.
	if len(r.FilePaths) == len(info.Files) {
		meta.RestoreFilePaths(info.Files, r.FilePaths)
	}

	for _, fileIndex := range r.SelectedFiles {
		if fileIndex < 0 || fileIndex >= len(info.Files) {
			return nil, fmt.Errorf("invalid selected file index %d", fileIndex)
		}
	}
	selectedFilesSet, err := newSelectedFilesSet(len(info.Files), r.SelectedFiles)
	if err != nil {
		return nil, err
	}
	completedBm := bm.FromBitfields(r.Bitfield, info.NumPieces)
	if err := validateResumeFiles(r.BasePath, info, selectedFilesSet, completedBm); err != nil {
		return nil, err
	}

	wantedBm := wantedPiecesForSelection(info, selectedFilesSet)
	complete := wantedBm.WithAndNot(completedBm).Count() == 0
	state := Downloading
	if r.State == ResumeStopped {
		state = Stopped
	} else if complete {
		state = Seeding
	}
	if state == Downloading {
		trackerStagger = min(trackerStagger, 60*time.Second)
	}

	// Restore piece pick strategy from resume.
	// Clamp unknown values to rarest-first.
	s := r.PiecePickStrategy
	if s > 1 {
		s = 0
	}

	return New(sess, m, info, r.BasePath, r.Tags, r.Custom, r.SelectedFiles, InitState{
		CompletedPieces:   completedBm,
		State:             state,
		PiecePickStrategy: PiecePickStrategy(s),
		TrackerStagger:    trackerStagger,
		resume: &resumeInitState{
			addAt:              r.AddAt.Time,
			completedAt:        r.CompletedAt.Time,
			trackers:           metainfo.AnnounceList(r.Trackers),
			trackerKey:         r.TrackerKey,
			downloaded:         r.Downloaded,
			uploaded:           r.Uploaded,
			corrupted:          r.Corrupted,
			downloadSpeedLimit: r.DownloadSpeedLimit,
			uploadSpeedLimit:   r.UploadSpeedLimit,
			queueWeight:        r.QueueWeight,
		},
	})
}

func wantedPiecesForSelection(info meta.Info, selectedFilesSet *bm.Bitmap) *bm.Bitmap {
	wantedBm := bm.New(info.NumPieces)
	if selectedFilesSet.Count() == uint32(len(info.Files)) {
		wantedBm.Fill()
		return wantedBm
	}
	for pieceIndex := range info.NumPieces {
		for chunk := range info.PieceFileChunks(pieceIndex) {
			if selectedFilesSet.Contains(uint32(chunk.FileIndex)) {
				wantedBm.Set(pieceIndex)
				break
			}
		}
	}
	return wantedBm
}

func validateResumeFiles(
	basePath string,
	info meta.Info,
	selectedFilesSet *bm.Bitmap,
	completedBm *bm.Bitmap,
) error {
	fileSizes := make(map[int]int64, len(info.Files)+1)
	for i, tf := range info.Files {
		if !selectedFilesSet.Contains(uint32(i)) {
			continue
		}
		p := filepath.Join(basePath, tf.Path)
		stat, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errgo.Wrap(err, fmt.Sprintf("failed to stat %q", tf.Path))
		}
		if stat.Size() > 0 {
			fileSizes[i] = stat.Size()
		}
	}

	for i := range info.NumPieces {
		if !completedBm.Contains(i) {
			continue
		}
		for chunk := range info.PieceFileChunks(i) {
			fileSize, ok := fileSizes[chunk.FileIndex]
			if !ok || chunk.OffsetOfFile+chunk.Length > fileSize {
				completedBm.Unset(i)
				break
			}
		}
	}
	return nil
}

// TrkStagger calls Stagger on the download's tracker set.
func (d *Download) TrkStagger(maxDelay time.Duration) {
	d.tracker.Stagger(maxDelay)
}
