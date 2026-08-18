// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package client

import (
	"context"
	"crypto/sha1"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"neptune/internal/config"
	"neptune/internal/meta"
	"neptune/internal/metainfo"
	"neptune/internal/session/store"
)

func randomPort(t *testing.T) uint16 {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ":0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return uint16(port)
}

// writeTorrentFile writes a valid .torrent for a single 4-block piece file.
func writeTorrentFile(t *testing.T, sessionPath, name string) metainfo.Hash {
	t.Helper()
	pieceLength := int64(4 * 16 * 1024)
	digest := sha1.Sum(make([]byte, pieceLength))
	infoBytes, err := bencode.Marshal(metainfo.Info{
		Name:        name,
		Pieces:      digest[:],
		PieceLength: pieceLength,
		Length:      pieceLength,
	})
	require.NoError(t, err)

	var m = &metainfo.MetaInfo{InfoBytes: infoBytes}
	info, err := meta.FromTorrent(*m)
	require.NoError(t, err)

	hash := info.Hash.Hex()
	dir := filepath.Join(sessionPath, "torrents", hash[:2], hash[2:4])
	require.NoError(t, os.MkdirAll(dir, 0o755))
	torrentBytes, err := bencode.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, hash+".torrent"), torrentBytes, 0o644))
	return info.Hash
}

// resetMetrics clears the global prometheus registry so InitMetrics (called
// from New) can register collectors again.
func resetMetrics() {
	prometheus.DefaultRegisterer = prometheus.NewRegistry()
	prometheus.DefaultGatherer = prometheus.DefaultRegisterer.(prometheus.Gatherer)
}

// TestStartMigratesAndLoadsLegacyResume simulates a first startup on a session
// directory that still holds legacy .resume files: they are imported into the
// store, removed from disk, and the download is restored.
func TestStartMigratesAndLoadsLegacyResume(t *testing.T) {
	resetMetrics()
	sessionPath := t.TempDir()
	hash := writeTorrentFile(t, sessionPath, "test.data")

	resumeData, err := bencode.Marshal(store.Resume{
		BasePath: filepath.Join(t.TempDir(), "data"),
		InfoHash: hash.Hex(),
		State:    store.ResumeStopped,
	})
	require.NoError(t, err)
	resumeDir := filepath.Join(sessionPath, "resume", hash.Hex()[:2])
	require.NoError(t, os.MkdirAll(resumeDir, 0o755))
	resumeFile := filepath.Join(resumeDir, hash.Hex()+".resume")
	require.NoError(t, os.WriteFile(resumeFile, resumeData, 0o644))

	cfg := config.Config{App: config.Application{
		P2PPort:                randomPort(t),
		MaxHTTPParallel:        4,
		GlobalConnectionLimit:  100,
		TorrentConnectionLimit: 20,
	}}
	c := New(cfg, sessionPath, false)
	require.NoError(t, c.Start())
	t.Cleanup(c.Shutdown)

	c.m.RLock()
	require.Len(t, c.downloads, 1)
	require.Equal(t, hash, c.downloads[0].InfoHash())
	c.m.RUnlock()

	// Legacy file removed and resume directory pruned.
	_, err = os.Stat(resumeFile)
	require.True(t, os.IsNotExist(err))

	// Store holds the imported row.
	all, err := c.session.Store.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, hash.Hex(), all[0].InfoHash)
}

// TestStartWithoutLegacyFiles ensures a session without .resume files starts
// cleanly and loads from the (empty) store.
func TestStartWithoutLegacyFiles(t *testing.T) {
	resetMetrics()
	sessionPath := t.TempDir()
	cfg := config.Config{App: config.Application{
		P2PPort:                randomPort(t),
		MaxHTTPParallel:        4,
		GlobalConnectionLimit:  100,
		TorrentConnectionLimit: 20,
	}}
	c := New(cfg, sessionPath, false)
	require.NoError(t, c.Start())
	t.Cleanup(c.Shutdown)

	c.m.RLock()
	require.Empty(t, c.downloads)
	c.m.RUnlock()
}
