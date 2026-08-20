// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trim21/go-bencode"

	"neptune/internal/session"
	"neptune/internal/session/store"
)

func newTestClientWithStore(t *testing.T) *Client {
	t.Helper()
	sessionPath := t.TempDir()
	st, err := store.Open(sessionPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return &Client{session: &session.Session{
		SessionPath: sessionPath,
		Store:       st,
	}}
}

func writeLegacyResume(t *testing.T, resumeDir, hash string) string {
	t.Helper()
	data, err := bencode.Marshal(store.Resume{
		BasePath: "/data",
		InfoHash: hash,
		State:    store.ResumeStopped,
	})
	require.NoError(t, err)

	dir := filepath.Join(resumeDir, hash[:2])
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, hash+".resume")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestMigrateLegacyResumeImportsAndRemoves(t *testing.T) {
	c := newTestClientWithStore(t)
	resumeDir := filepath.Join(c.session.SessionPath, "resume")

	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	file := writeLegacyResume(t, resumeDir, hash)

	require.NoError(t, c.migrateLegacyResume())

	all, err := c.session.Store.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, hash, all[0].InfoHash)
	require.Equal(t, "/data", all[0].BasePath)
	require.Equal(t, store.ResumeStopped, all[0].State)

	_, err = os.Stat(file)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(resumeDir)
	require.True(t, os.IsNotExist(err))
}

func TestMigrateLegacyResumeSkipsCorruptFile(t *testing.T) {
	c := newTestClientWithStore(t)
	resumeDir := filepath.Join(c.session.SessionPath, "resume")
	require.NoError(t, os.MkdirAll(resumeDir, 0o755))

	corruptFile := filepath.Join(resumeDir, "corrupt.resume")
	require.NoError(t, os.WriteFile(corruptFile, []byte("not a bencode file"), 0o644))

	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	writeLegacyResume(t, resumeDir, hash)

	require.NoError(t, c.migrateLegacyResume())

	all, err := c.session.Store.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, hash, all[0].InfoHash)
}

func TestMigrateLegacyResumeNoLegacyFiles(t *testing.T) {
	c := newTestClientWithStore(t)

	require.NoError(t, c.migrateLegacyResume())

	n, err := c.session.Store.Count()
	require.NoError(t, err)
	require.Zero(t, n)
}
