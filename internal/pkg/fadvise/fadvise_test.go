// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package fadvise

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDontNeed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	_, err = f.Write(make([]byte, 4096))
	require.NoError(t, err)

	// Should not error on any platform.
	err = DontNeed(f, 0, 4096)
	require.NoError(t, err)
}

func TestWillNeed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	err = WillNeed(f, 0, 0)
	require.NoError(t, err)
}

func TestSequential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	err = Sequential(f, 0, 0)
	require.NoError(t, err)
}

func TestRandom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	err = Random(f, 0, 0)
	require.NoError(t, err)
}

func TestNoReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	err = NoReuse(f, 0, 0)
	require.NoError(t, err)
}

func TestFadvise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	_, err = f.Write(make([]byte, 8192))
	require.NoError(t, err)

	// Test with explicit advice constant.
	err = Fadvise(f.Fd(), 0, 4096, AdvDontNeed)
	require.NoError(t, err)

	err = Fadvise(f.Fd(), 4096, 4096, AdvWillNeed)
	require.NoError(t, err)

	// Test with zero length (means to end of file on Linux, no-op on others).
	err = Fadvise(f.Fd(), 0, 0, AdvDontNeed)
	require.NoError(t, err)
}
