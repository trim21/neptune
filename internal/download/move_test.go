// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
