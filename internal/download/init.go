// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/go-units"
	"github.com/dustin/go-humanize"
	"github.com/juju/ratelimit"
	"github.com/trim21/errgo"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/fadvise"
	"neptune/internal/pkg/global"
)

type existingFile struct {
	index int
	size  int64
}

// CheckExistingFiles pre-allocates files, hash-checks existing data, and
// returns a bitmap of verified pieces. selected determines which files
// are considered for the download.
func CheckExistingFiles(ctx context.Context, info meta.Info, basePath string, selected *bm.Bitmap, fallocate bool) (*bm.Bitmap, error) {
	if err := os.MkdirAll(basePath, os.ModePerm); err != nil {
		return nil, err
	}

	var efs = make(map[int]*existingFile, len(info.Files)+1)
	for i, tf := range info.Files {
		f, e := tryAllocFile(i, filepath.Join(basePath, tf.Path), tf.Length, fallocate, selected.Contains(uint32(i)))
		if e != nil {
			return nil, e
		}
		if f != nil {
			efs[i] = f
		}
	}

	h := buildPieceToCheck(info, efs)
	if len(h) == 0 {
		return bm.New(info.NumPieces), nil
	}

	completedBm := bm.New(info.NumPieces)
	sum := sha1.New()

	var w io.Writer = sum

	if global.Dev {
		bucket := ratelimit.NewBucketWithQuantum(time.Second/10, units.MiB*100, units.MiB*100)
		w = ratelimit.Writer(sum, bucket)
	}

	var sha1Sum = make([]byte, sha1.Size)

	var buf [256 * 1024]byte
	var currentFile *os.File
	var currentFileIndex = -1

	defer func() {
		if currentFile != nil {
			currentFile.Close()
		}
	}()

	for _, pieceIndex := range h {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		for chunk := range info.PieceFileChunks(pieceIndex) {
			if chunk.FileIndex != currentFileIndex {
				if currentFile != nil {
					currentFile.Close()
					currentFile = nil
				}
				p := filepath.Join(basePath, info.Files[chunk.FileIndex].Path)
				f, err := os.OpenFile(p, os.O_RDONLY, 0)
				if err != nil {
					return nil, errgo.Wrap(err, fmt.Sprintf("failed to open file %q", p))
				}
				_ = fadvise.Sequential(f, 0, 0)
				currentFile = f
				currentFileIndex = chunk.FileIndex
			}

			remaining := chunk.Length
			off := chunk.OffsetOfFile
			for remaining > 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}

				toRead := min(remaining, int64(len(buf)))
				n, err := currentFile.ReadAt(buf[:toRead], off)
				if n > 0 {
					if _, werr := w.Write(buf[:n]); werr != nil {
						return nil, errgo.Wrap(werr, "failed to hash data")
					}
					off += int64(n)
					remaining -= int64(n)
				}
				if err != nil {
					if err == io.EOF && remaining == 0 {
						break
					}
					return nil, errgo.Wrap(err, "failed to read file "+currentFile.Name())
				}
			}
		}
		sha1Sum = sum.Sum(sha1Sum[:0])
		if [sha1.Size]byte(sha1Sum[:sha1.Size]) == info.Pieces[pieceIndex] {
			completedBm.Set(pieceIndex)
		}

		sum.Reset()
	}

	return completedBm, nil
}

func (d *Download) initCheck() error {
	completedBm, err := CheckExistingFiles(d.ctx, d.info, d.s.basePath, d.selectedFilesSet, d.session.Config.App.Fallocate)
	if err != nil {
		return err
	}

	// Merge verified pieces into the download's bitmap.
	d.completedBm.OR(completedBm)
	d.setMissingFromWantedSync()
	d.completed.Store(d.computeCompletedUnsafe())

	d.pieceDownloadRate.Reset()
	donePieces := d.completedBm.WithAnd(d.wantedBm).Count()
	d.log.Debug().Msgf("done size %s", humanize.IBytes(uint64(donePieces)*uint64(d.info.PieceLength)))

	return nil
}

func buildPieceToCheck(info meta.Info, efs map[int]*existingFile) []uint32 {
	if len(efs) == 0 {
		return nil
	}

	var r = make([]uint32, 0, info.NumPieces)

	for i := range info.NumPieces {
		shouldCheck := true
		for chunk := range info.PieceFileChunks(i) {
			ef, ok := efs[chunk.FileIndex]
			if !ok || chunk.OffsetOfFile > ef.size || chunk.OffsetOfFile+chunk.Length > ef.size {
				shouldCheck = false
				break
			}
		}

		if shouldCheck {
			r = append(r, i)
		}
	}

	return r
}

func tryAllocFile(index int, path string, size int64, doAlloc bool, selected bool) (*existingFile, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		// file not exists

		if err = os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
			return nil, err
		}

		f, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		return nil, f.Truncate(size)
	}

	var ef *existingFile
	fs := stat.Size()
	if fs != 0 {
		ef = &existingFile{index: index, size: fs}
	}

	if doAlloc && selected && fs != size {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		return nil, f.Truncate(size)
	}

	return ef, nil
}

// verifyFileSizesStandalone checks that all selected files exist with matching sizes.
func verifyFileSizesStandalone(info meta.Info, basePath string, selected *bm.Bitmap) error {
	for i, tf := range info.Files {
		if !selected.Contains(uint32(i)) {
			continue
		}

		p := filepath.Join(basePath, tf.Path)
		stat, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("file %q does not exist", tf.Path)
			}
			return errgo.Wrap(err, fmt.Sprintf("failed to stat file %q", tf.Path))
		}

		if stat.Size() != tf.Length {
			return fmt.Errorf("file %q size mismatch: expected %d, got %d", tf.Path, tf.Length, stat.Size())
		}
	}
	return nil
}

// verifyFileSizes checks that all selected files exist with matching sizes.
// No SHA-1 piece verification is performed. Bitmap is not modified.
func (d *Download) verifyFileSizes() error {
	return verifyFileSizesStandalone(d.info, d.s.basePath, d.selectedFilesSet)
}

func (d *Download) checkNew(skipHashCheck bool) {
	d.log.Debug().Msg("initializing download")

	if skipHashCheck {
		if err := d.verifyFileSizes(); err != nil {
			d.setError(err)
			d.log.Err(err).Msg("file size verification failed")
			return
		}
		d.completedBm.Fill()
		d.missingBm.Clear()
	} else {
		if err := d.initCheck(); err != nil {
			d.setError(err)
			d.log.Err(err).Msg("failed to initCheck torrent data")
			return
		}
	}

	d.setMissingFromWantedSync()
	d.completed.Store(d.computeCompletedUnsafe())
	donePieces := d.completedBm.WithAnd(d.wantedBm).Count()
	d.pieceDownloadRate.Reset()

	d.log.Debug().Msgf("done size %s", humanize.IBytes(uint64(donePieces)*uint64(d.info.PieceLength)))

	allDone := d.isComplete()
	if allDone {
		d.completedAt.Store(time.Now().UnixNano())
		if _, err := d.transition(Seeding); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state after init check")
		}
	} else {
		transition, err := d.transition(Downloading)
		if err != nil {
			d.log.Error().Err(err).Msg("failed to transition state after init check")
		} else if transition.changed {
			d.fireStartedHook()
		}
	}
}
