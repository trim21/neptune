// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/gfs"
	"neptune/internal/pkg/ratelimit"
	"neptune/internal/session"
)

type resumeTestFixture struct {
	sess     *session.Session
	metainfo *metainfo.MetaInfo
	basePath string
	info     meta.Info
}

func newResumeTestFixture(t *testing.T, numPieces uint32) resumeTestFixture {
	t.Helper()

	root := t.TempDir()
	ioc := gfs.NewIOContext()
	t.Cleanup(ioc.Close)
	sess := &session.Session{
		FilePool:          filepool.New(),
		IOContext:         ioc,
		DownloadLimiter:   ratelimit.New(0),
		UploadLimiter:     ratelimit.New(0),
		PieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		PieceUploadRate:   flowrate.New(time.Second, 5*time.Second),
		ResumePath:        filepath.Join(root, "resume"),
		TorrentPath:       filepath.Join(root, "torrents"),
	}

	pieceLength := int64(4 * defaultBlockSize)
	digest := sha1.Sum(make([]byte, pieceLength))
	pieces := make([]byte, 0, int(numPieces)*sha1.Size)
	for range numPieces {
		pieces = append(pieces, digest[:]...)
	}
	infoBytes, err := bencode.Marshal(metainfo.Info{
		Name:        "test.data",
		Pieces:      pieces,
		PieceLength: pieceLength,
		Length:      int64(numPieces) * pieceLength,
	})
	require.NoError(t, err)
	m := &metainfo.MetaInfo{InfoBytes: infoBytes}
	info, err := meta.FromTorrent(*m)
	require.NoError(t, err)

	torrentBytes, err := bencode.Marshal(m)
	require.NoError(t, err)
	hash := info.Hash.Hex()
	torrentDir := filepath.Join(sess.TorrentPath, hash[:2], hash[2:4])
	require.NoError(t, os.MkdirAll(torrentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(torrentDir, hash+".torrent"), torrentBytes, 0o644))

	return resumeTestFixture{
		sess:     sess,
		metainfo: m,
		info:     info,
		basePath: filepath.Join(root, "data"),
	}
}

func (f resumeTestFixture) resumeData(t *testing.T, state ResumeState, completedPieces ...uint32) []byte {
	t.Helper()
	completed := bm.New(f.info.NumPieces)
	for _, pieceIndex := range completedPieces {
		completed.Set(pieceIndex)
	}
	data, err := bencode.Marshal(resume{
		BasePath: f.basePath,
		InfoHash: f.info.Hash.Hex(),
		Bitfield: completed.Bitfield(),
		State:    state,
	})
	require.NoError(t, err)
	return data
}

func (f resumeTestFixture) load(t *testing.T, data []byte) *Download {
	t.Helper()
	d, err := LoadFromResume(f.sess, data, 0)
	require.NoError(t, err)
	t.Cleanup(d.Close)
	return d
}

func (f resumeTestFixture) writeDataFile(t *testing.T) {
	t.Helper()
	path := filepath.Join(f.basePath, f.info.Files[0].Path)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, f.info.TotalLength), 0o644))
}

func TestNewRejectsInconsistentInitState(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	d, err := New(f.sess, f.metainfo, f.info, f.basePath, nil, nil, nil, InitState{State: Seeding})
	require.Error(t, err)
	require.Nil(t, d)
}

func TestNewCheckingCanCloseImmediately(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	d, err := New(f.sess, f.metainfo, f.info, f.basePath, nil, nil, nil, InitState{
		State:             Checking,
		PiecePickStrategy: StrategyRarestFirst,
	})
	require.NoError(t, err)
	d.Close()
}

func TestLoadFromResumeValidatesBeforeCreatingPicker(t *testing.T) {
	f := newResumeTestFixture(t, 2)
	d := f.load(t, f.resumeData(t, ResumeActive, 0))

	require.Equal(t, Downloading, d.GetState())
	require.Zero(t, d.completedBm.Count())
	require.Equal(t, 2, d.missingBm.Count())
	picker := d.picker.Load()
	require.NotNil(t, picker)

	peerPieces := bm.NewLockFreeBitmap(d.info.NumPieces)
	peerPieces.Set(0)
	claims := picker.PickAndClaim(nil, PickRequest{
		Bitfield:  peerPieces,
		PeerID:    1,
		NumBlocks: d.info.PieceBlockCount(0),
	})
	require.NotEmpty(t, claims)
	require.Equal(t, uint32(0), claims[0].Block.PieceIndex)
}

func TestLoadFromResumeInvalidSeedBecomesDownloading(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	d := f.load(t, f.resumeData(t, ResumeActive, 0))

	require.Equal(t, Downloading, d.GetState())
	require.NotNil(t, d.picker.Load())
	require.False(t, d.completedBm.Contains(0))
	require.True(t, d.missingBm.Contains(0))
}

func TestLoadFromResumeCompleteSeed(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	f.writeDataFile(t)
	d := f.load(t, f.resumeData(t, ResumeActive, 0))

	require.Equal(t, Seeding, d.GetState())
	require.Nil(t, d.picker.Load())
	require.True(t, d.completedBm.Contains(0))
	require.Zero(t, d.missingBm.Count())
}

func TestLoadFromResumePreservesStoppedIntent(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	d := f.load(t, f.resumeData(t, ResumeStopped))

	require.Equal(t, Stopped, d.GetState())
	require.NotNil(t, d.picker.Load())
}

func TestLoadFromResumeExcludesValidatedCompletedPieceFromPicker(t *testing.T) {
	f := newResumeTestFixture(t, 2)
	f.writeDataFile(t)
	d := f.load(t, f.resumeData(t, ResumeActive, 0))

	require.Equal(t, Downloading, d.GetState())
	require.True(t, d.completedBm.Contains(0))
	require.False(t, d.missingBm.Contains(0))
	require.True(t, d.missingBm.Contains(1))

	peerPieces := bm.NewLockFreeBitmap(d.info.NumPieces)
	peerPieces.Set(0)
	require.Empty(t, d.picker.Load().PickAndClaim(nil, PickRequest{
		Bitfield:  peerPieces,
		PeerID:    1,
		NumBlocks: d.info.PieceBlockCount(0),
	}))
}
