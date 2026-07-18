// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"neptune/internal/meta"
	"neptune/internal/piece_store"
)

var errMoveTest = errors.New("move failed")

type moveTestStore struct {
	piece_store.Store
	move func(context.Context, string, piece_store.MoveProgressFunc) error
}

func (s *moveTestStore) Move(ctx context.Context, target string, report piece_store.MoveProgressFunc) error {
	return s.move(ctx, target, report)
}

func TestMoveFailureRestoresStateAndReturnsError(t *testing.T) {
	d := newTestDownload(t, 1, 1, func(info meta.Info) piece_store.PieceStore {
		return &moveTestStore{
			Store: piece_store.NewMemStore(info),
			move: func(context.Context, string, piece_store.MoveProgressFunc) error {
				return errMoveTest
			},
		}
	})

	err := d.RequestMove(t.TempDir())
	require.ErrorIs(t, err, errMoveTest)
	require.Equal(t, Downloading, d.GetState())
	require.Empty(t, d.ErrorMsg())
	require.False(t, d.MoveStatus().Active)
}

func TestStopCancelsMoveAndKeepsStoppedState(t *testing.T) {
	started := make(chan struct{})
	d := newTestDownload(t, 1, 1, func(info meta.Info) piece_store.PieceStore {
		return &moveTestStore{
			Store: piece_store.NewMemStore(info),
			move: func(ctx context.Context, _ string, report piece_store.MoveProgressFunc) error {
				report(piece_store.MoveProgress{Phase: piece_store.MoveCopying})
				close(started)
				<-ctx.Done()
				return ctx.Err()
			},
		}
	})

	done := make(chan error, 1)
	go func() {
		done <- d.RequestMove(t.TempDir())
	}()
	<-started
	require.NoError(t, d.Stop())
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, Stopped, d.GetState())
	require.False(t, d.MoveStatus().Active)
}

func TestPruneEmptyDirectories_EmptyDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "empty-dir")
	require.NoError(t, os.MkdirAll(dir, 0755))

	err := pruneEmptyDir(dir)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "empty directory should be removed")
}

func TestPruneEmptyDirectories_NonEmptyDir_WithFile(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "non-empty")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0644))

	err := pruneEmptyDir(dir)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	require.NoError(t, err, "non-empty directory should remain")
	_, err = os.Stat(filepath.Join(dir, "test.txt"))
	require.NoError(t, err, "file in non-empty dir should remain")
}

func TestPruneEmptyDirectories_NestedEmptyDirs(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "a", "b", "c")
	require.NoError(t, os.MkdirAll(dir, 0755))

	err := pruneEmptyDir(dir)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "deepest empty dir should be removed")
}

func TestPruneEmptyDirectories_NonExistentDir(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "does-not-exist")

	err := pruneEmptyDir(dir)
	require.Error(t, err)
}

func TestPruneEmptyDirectories_RemoveFileThenPrune(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "resume", "ab")
	require.NoError(t, os.MkdirAll(dir, 0755))
	f := filepath.Join(dir, "hash.resume")
	require.NoError(t, os.WriteFile(f, []byte("data"), 0644))

	require.NoError(t, os.Remove(f))
	err := pruneEmptyDir(dir)
	require.NoError(t, err)

	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "directory should be removed after file deleted")
}
