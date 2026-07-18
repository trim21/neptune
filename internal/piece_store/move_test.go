// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package piece_store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/filepool"
	"neptune/internal/pkg/gfs"
)

func TestFileStoreMoveMovesAllExistingFiles(t *testing.T) {
	info := moveTestInfo([]meta.File{
		{Path: "a/data", Length: 8 * 1024},
		{Path: "b/data", Length: 8 * 1024},
	})
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	store := newMoveTestStore(t, info, source, []uint32{0})
	data := bytes.Repeat([]byte("a"), int(info.TotalLength))

	if err := store.WriteChunk(context.Background(), 0, 0, data); err != nil {
		t.Fatal(err)
	}

	var snapshots []MoveProgress
	err := store.Move(context.Background(), target, func(progress MoveProgress) {
		snapshots = append(snapshots, progress)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, file := range info.Files {
		if _, err = os.Stat(filepath.Join(source, file.Path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source file %q still exists: %v", file.Path, err)
		}
		var moved []byte
		moved, err = os.ReadFile(filepath.Join(target, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		if len(moved) != int(file.Length) {
			t.Fatalf("moved file %q has length %d, want %d", file.Path, len(moved), file.Length)
		}
	}

	read := make([]byte, len(data))
	n, err := store.ReadChunk(context.Background(), 0, 0, read)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) || !bytes.Equal(read, data) {
		t.Fatal("store did not read moved data from the target path")
	}

	last := snapshots[len(snapshots)-1]
	if last.Phase != MoveCleaning || last.FilesDone != 2 || last.FilesTotal != 2 {
		t.Fatalf("final progress = %+v", last)
	}
	if last.BytesDone != info.TotalLength || last.BytesTotal != info.TotalLength {
		t.Fatalf("final byte progress = %d/%d, want %d/%d", last.BytesDone, last.BytesTotal, info.TotalLength, info.TotalLength)
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].BytesDone < snapshots[i-1].BytesDone {
			t.Fatalf("progress moved backwards: %+v then %+v", snapshots[i-1], snapshots[i])
		}
	}
}

func TestFileStoreMoveTargetConflictKeepsSourceAuthoritative(t *testing.T) {
	info := moveTestInfo([]meta.File{{Path: "data", Length: 4 * 1024}})
	source := t.TempDir()
	target := t.TempDir()
	store := newMoveTestStore(t, info, source, nil)
	data := bytes.Repeat([]byte("s"), int(info.TotalLength))
	if err := store.WriteChunk(context.Background(), 0, 0, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data"), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := store.Move(context.Background(), target, nil); err == nil {
		t.Fatal("Move succeeded despite an existing target")
	}

	read := make([]byte, len(data))
	n, err := store.ReadChunk(context.Background(), 0, 0, read)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) || !bytes.Equal(read, data) {
		t.Fatal("source data was not authoritative after failed move")
	}
	if got, err := os.ReadFile(filepath.Join(target, "data")); err != nil || string(got) != "target" {
		t.Fatalf("existing target was modified: %q, %v", got, err)
	}
}

func TestFileStoreMoveWaitsForIOAndNewWriteUsesTarget(t *testing.T) {
	info := moveTestInfo([]meta.File{{Path: "data", Length: 4 * 1024}})
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	store := newMoveTestStore(t, info, source, nil)
	oldData := bytes.Repeat([]byte("o"), int(info.TotalLength))
	newData := bytes.Repeat([]byte("n"), int(info.TotalLength))
	if err := store.WriteChunk(context.Background(), 0, 0, oldData); err != nil {
		t.Fatal(err)
	}

	store.opMu.RLock()
	waiting := make(chan struct{})
	moveDone := make(chan error, 1)
	go func() {
		moveDone <- store.Move(context.Background(), target, func(progress MoveProgress) {
			if progress.Phase == MoveWaiting {
				select {
				case <-waiting:
				default:
					close(waiting)
				}
			}
		})
	}()
	<-waiting

	select {
	case err := <-moveDone:
		t.Fatalf("Move passed an active store operation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.WriteChunk(context.Background(), 0, 0, newData)
	}()
	time.Sleep(20 * time.Millisecond)
	store.opMu.RUnlock()

	if err := <-moveDone; err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a blocked write recreated the source file: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(target, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newData) {
		t.Fatal("blocked write did not resume on the target path")
	}
}

func TestFileStoreMoveCancellationWhileWaitingKeepsSource(t *testing.T) {
	info := moveTestInfo([]meta.File{{Path: "data", Length: 1024}})
	source := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	store := newMoveTestStore(t, info, source, nil)
	data := bytes.Repeat([]byte("d"), int(info.TotalLength))
	if err := store.WriteChunk(context.Background(), 0, 0, data); err != nil {
		t.Fatal(err)
	}

	store.opMu.RLock()
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- store.Move(ctx, target, func(progress MoveProgress) {
			if progress.Phase == MoveWaiting {
				close(waiting)
			}
		})
	}()
	<-waiting
	cancel()
	store.opMu.RUnlock()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Move error = %v, want context.Canceled", err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "data")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("source changed after cancellation: %v", err)
	}
}

func moveTestInfo(files []meta.File) meta.Info {
	var total int64
	for _, file := range files {
		total += file.Length
	}
	info := meta.Info{
		Name:          "move-test",
		Files:         files,
		TotalLength:   total,
		PieceLength:   total,
		LastPieceSize: total,
		NumPieces:     1,
	}
	initFileOffsets(&info)
	return info
}

func newMoveTestStore(t *testing.T, info meta.Info, basePath string, selected []uint32) *FileStore {
	t.Helper()
	for _, file := range info.Files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(basePath, file.Path)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	ioc := gfs.NewIOContext()
	t.Cleanup(ioc.Close)
	selectedFiles := bm.New(uint32(len(info.Files)))
	if selected == nil {
		selectedFiles.Fill()
	} else {
		for _, index := range selected {
			selectedFiles.Set(index)
		}
	}
	store := NewFileStore(info, basePath, filepool.New(), ioc, selectedFiles, false)
	t.Cleanup(func() {
		paths := make([]string, 0, len(store.info.Files))
		for fileIndex := range store.info.Files {
			paths = append(paths, store.filePath(fileIndex))
		}
		store.fp.InvalidatePaths(paths)
	})
	return store
}
