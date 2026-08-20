// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package store

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/timestamp"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrateFreshDatabase(t *testing.T) {
	s := openTestStore(t)

	n, err := s.Count()
	require.NoError(t, err)
	require.Zero(t, n)

	var v int
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v))
	require.Equal(t, migrations[len(migrations)-1].version, v)
}

func TestMigrateUpgradesOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `PRAGMA user_version = 0`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	n, err := s.Count()
	require.NoError(t, err)
	require.Zero(t, n)

	var v int
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&v))
	require.Equal(t, migrations[len(migrations)-1].version, v)
}

func TestMigrateIdempotentReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.Upsert(&Resume{InfoHash: "h", BasePath: "/a"}))

	// Reopening applies no migrations and keeps data.
	s2, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	all, err := s2.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "h", all[0].InfoHash)
}

// TestCrashRecovery simulates a hard kill (os.Exit bypasses deferred Close and
// leaves WAL uncheckpointed) and verifies the database still loads on reopen.
func TestCrashRecovery(t *testing.T) {
	if os.Getenv("STORE_CRASH_CHILD") == "1" {
		s, err := Open(os.Getenv("STORE_CRASH_DIR"))
		if err != nil {
			os.Exit(1)
		}
		_ = s.Upsert(&Resume{InfoHash: "crashed", BasePath: "/data"})
		os.Exit(1) //nolint:revive // simulate process crash
	}

	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestCrashRecovery")
	cmd.Env = append(os.Environ(), "STORE_CRASH_CHILD=1", "STORE_CRASH_DIR="+dir)
	require.Error(t, cmd.Run()) // child exits non-zero

	s, err := Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "crashed", all[0].InfoHash)

	require.NoError(t, s.Upsert(&Resume{InfoHash: "crashed", BasePath: "/data2"}))
	all, err = s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "/data2", all[0].BasePath)
}

func TestUpsertAllRoundTrip(t *testing.T) {
	s := openTestStore(t)

	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	want := Resume{
		BasePath:           "/data",
		InfoHash:           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Bitfield:           []byte{0xff, 0x00, 0x55},
		Tags:               []string{"tag1", "tag2"},
		Custom:             map[string]string{"k1": "v1"},
		Trackers:           [][]string{{"http://a"}, {"http://b", "http://c"}},
		SelectedFiles:      []int{0, 2, 4},
		FilePaths:          []string{"a.txt", "b.txt"},
		DownloadSpeedLimit: 1024,
		UploadSpeedLimit:   2048,
		AddAt:              timestamp.New(at),
		CompletedAt:        timestamp.New(at.Add(time.Hour)),
		Downloaded:         1000,
		Uploaded:           2000,
		Corrupted:          3000,
		TrackerKey:         "key",
		State:              ResumeActive,
		PiecePickStrategy:  1,
		QueueWeight:        42,
	}
	require.NoError(t, s.Upsert(&want))

	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	got := all[0]
	require.Equal(t, want.BasePath, got.BasePath)
	require.Equal(t, want.InfoHash, got.InfoHash)
	require.Equal(t, want.Bitfield, got.Bitfield)
	require.Equal(t, want.Tags, got.Tags)
	require.Equal(t, want.Custom, got.Custom)
	require.Equal(t, want.Trackers, got.Trackers)
	require.Equal(t, want.SelectedFiles, got.SelectedFiles)
	require.Equal(t, want.FilePaths, got.FilePaths)
	require.Equal(t, want.DownloadSpeedLimit, got.DownloadSpeedLimit)
	require.Equal(t, want.UploadSpeedLimit, got.UploadSpeedLimit)
	require.True(t, want.AddAt.Equal(got.AddAt.Time))
	require.True(t, want.CompletedAt.Equal(got.CompletedAt.Time))
	require.Equal(t, want.Downloaded, got.Downloaded)
	require.Equal(t, want.Uploaded, got.Uploaded)
	require.Equal(t, want.Corrupted, got.Corrupted)
	require.Equal(t, want.TrackerKey, got.TrackerKey)
	require.Equal(t, want.State, got.State)
	require.Equal(t, want.PiecePickStrategy, got.PiecePickStrategy)
	require.Equal(t, want.QueueWeight, got.QueueWeight)

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestUpsertOverwritesSameInfoHash(t *testing.T) {
	s := openTestStore(t)

	r := &Resume{InfoHash: "h", BasePath: "/a", State: ResumeStopped}
	require.NoError(t, s.Upsert(r))

	r.BasePath = "/b"
	r.State = ResumeActive
	require.NoError(t, s.Upsert(r))

	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "/b", all[0].BasePath)
	require.Equal(t, ResumeActive, all[0].State)

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestSelectedFilesNilDistinctFromEmpty(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.Upsert(&Resume{InfoHash: "a", BasePath: "/a"}))
	require.NoError(t, s.Upsert(&Resume{InfoHash: "b", BasePath: "/b", SelectedFiles: []int{}}))

	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 2)

	var nilSel, emptySel Resume
	for _, r := range all {
		if r.InfoHash == "a" {
			nilSel = r
		} else {
			emptySel = r
		}
	}
	require.Nil(t, nilSel.SelectedFiles)
	require.Empty(t, emptySel.SelectedFiles)
	require.NotNil(t, emptySel.SelectedFiles)
}

func TestDelete(t *testing.T) {
	s := openTestStore(t)

	require.NoError(t, s.Upsert(&Resume{InfoHash: "a", BasePath: "/a"}))
	require.NoError(t, s.Upsert(&Resume{InfoHash: "b", BasePath: "/b"}))
	require.NoError(t, s.Delete("a"))

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n)

	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "b", all[0].InfoHash)
}

func TestNilFieldsRoundTrip(t *testing.T) {
	s := openTestStore(t)

	r := &Resume{InfoHash: "h", BasePath: "/a"}
	require.NoError(t, s.Upsert(r))

	all, err := s.All()
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Nil(t, all[0].Tags)
	require.Nil(t, all[0].Custom)
	require.Nil(t, all[0].Trackers)
	require.Nil(t, all[0].FilePaths)
	require.Nil(t, all[0].Bitfield)
	require.Empty(t, all[0].TrackerKey)
}
