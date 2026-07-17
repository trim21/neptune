// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
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

	"neptune/internal/pkg/fadvise"
	"neptune/internal/pkg/global"
)

type existingFile struct {
	index int
	size  int64
}

func (d *Download) initCheck() error {
	d.log.Debug().Msg("initCheck")

	if err := os.MkdirAll(d.s.basePath, os.ModePerm); err != nil {
		return err
	}

	d.log.Debug().Msg("try pre alloc")
	var efs = make(map[int]*existingFile, len(d.info.Files)+1)
	for i, tf := range d.info.Files {
		p := tf.Path
		f, e := tryAllocFile(i, filepath.Join(d.s.basePath, p), tf.Length, d.session.Config.App.Fallocate, d.isFileSelected(i))
		if e != nil {
			return e
		}
		if f != nil {
			efs[i] = f
		}
	}

	h := d.buildPieceToCheck(efs)
	if len(h) == 0 {
		return nil
	}

	d.log.Debug().Msg("start checking")

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
		if d.ctx.Err() != nil {
			return d.ctx.Err()
		}

		for chunk := range d.info.PieceFileChunks(pieceIndex) {
			if chunk.FileIndex != currentFileIndex {
				if currentFile != nil {
					currentFile.Close()
					currentFile = nil
				}
				p := filepath.Join(d.s.basePath, d.info.Files[chunk.FileIndex].Path)
				f, err := os.OpenFile(p, os.O_RDONLY, 0)
				if err != nil {
					return errgo.Wrap(err, fmt.Sprintf("failed to open file %q", p))
				}
				_ = fadvise.Sequential(f, 0, 0)
				currentFile = f
				currentFileIndex = chunk.FileIndex
			}

			remaining := chunk.Length
			off := chunk.OffsetOfFile
			for remaining > 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}

				toRead := min(remaining, int64(len(buf)))
				n, err := currentFile.ReadAt(buf[:toRead], off)
				if n > 0 {
					d.pieceDownloadRate.Update(n)
					if _, werr := w.Write(buf[:n]); werr != nil {
						return errgo.Wrap(werr, "failed to hash data")
					}
					off += int64(n)
					remaining -= int64(n)
				}
				if err != nil {
					if err == io.EOF && remaining == 0 {
						break
					}
					return errgo.Wrap(err, "failed to read file "+currentFile.Name())
				}
			}
		}
		sha1Sum = sum.Sum(sha1Sum[:0])
		if [sha1.Size]byte(sha1Sum[:sha1.Size]) == d.info.Pieces[pieceIndex] {
			d.completedBm.Set(pieceIndex)
			d.missingBm.Unset(pieceIndex)
			d.picker.Load().WeHave(pieceIndex)
			d.completed.Add(d.info.PieceLen(pieceIndex))
		}

		sum.Reset()
	}

	return nil
}

func (d *Download) buildPieceToCheck(efs map[int]*existingFile) []uint32 {
	if len(efs) == 0 {
		return nil
	}

	var r = make([]uint32, 0, d.info.NumPieces)

	for i := range d.info.NumPieces {
		shouldCheck := true
		for chunk := range d.info.PieceFileChunks(i) {
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

// validateResume checks that pieces marked complete in completedBm still have
// their backing files on disk. Pieces whose file data is missing or truncated
// are cleared from the bitmap.
func (d *Download) validateResume() error {
	var efs = make(map[int]*existingFile, len(d.info.Files)+1)
	for i, tf := range d.info.Files {
		if !d.isFileSelected(i) {
			continue
		}
		p := filepath.Join(d.s.basePath, tf.Path)
		stat, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errgo.Wrap(err, fmt.Sprintf("failed to stat %q", tf.Path))
		}
		if stat.Size() > 0 {
			efs[i] = &existingFile{index: i, size: stat.Size()}
		}
	}

	for i := range d.info.NumPieces {
		if !d.completedBm.Contains(i) {
			continue
		}
		valid := true
		for chunk := range d.info.PieceFileChunks(i) {
			ef, ok := efs[chunk.FileIndex]
			if !ok || chunk.OffsetOfFile+chunk.Length > ef.size {
				valid = false
				break
			}
		}
		if !valid {
			d.completedBm.Unset(i)
			d.missingBm.Set(i)
			d.completed.Add(-d.info.PieceLen(i))
		}
	}
	return nil
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

// verifyFileSizes checks that all selected files exist with matching sizes.
// No SHA-1 piece verification is performed. Bitmap is not modified.
func (d *Download) verifyFileSizes() error {
	d.log.Debug().Msg("verifyFileSizes")

	for i, tf := range d.info.Files {
		if !d.isFileSelected(i) {
			continue
		}

		p := filepath.Join(d.s.basePath, tf.Path)
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

func (d *Download) checkNew(skipHashCheck bool) {
	d.log.Debug().Msg("initializing download")
	d.state.Store(uint32(Checking))

	if skipHashCheck {
		if err := d.verifyFileSizes(); err != nil {
			d.setError(err)
			d.log.Err(err).Msg("file size verification failed")
			return
		}
		d.completedBm.Fill()
		d.missingBm.Clear()
		for i := range d.info.NumPieces {
			d.picker.Load().WeHave(i)
		}
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
