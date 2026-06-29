// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

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
	"neptune/internal/pkg/fallocate"
	"neptune/internal/pkg/gfs"
	"neptune/internal/pkg/global"
)

type existingFile struct {
	index int
	size  int64
}

func (d *Download) initCheck() error {
	d.log.Debug().Msg("initCheck")

	if err := os.MkdirAll(d.basePath, os.ModePerm); err != nil {
		return err
	}

	d.log.Debug().Msg("try pre alloc")
	var efs = make(map[int]*existingFile, len(d.info.Files)+1)
	for i, tf := range d.info.Files {
		p := tf.Path
		f, e := tryAllocFile(i, filepath.Join(d.basePath, p), tf.Length, d.c.Config.App.Fallocate, d.isFileSelected(i))
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

	for _, pieceIndex := range h {
		if d.ctx.Err() != nil {
			return d.ctx.Err()
		}

		for _, chunk := range d.pieceInfo.fileChunks(pieceIndex) {
			p := filepath.Join(d.basePath, d.info.Files[chunk.fileIndex].Path)
			f, err := os.OpenFile(p, os.O_RDONLY, 0)
			if err != nil {
				return errgo.Wrap(err, fmt.Sprintf("failed to open file %q", p))
			}
			// Init check scans files sequentially; tell the kernel to prefetch.
			_ = fadvise.Sequential(f, 0, 0)

			_, err = d.ioDown.IO64(gfs.CopyReaderAt(w, f, chunk.offsetOfFile, chunk.length))
			if err != nil {
				f.Close()
				return errgo.Wrap(err, "failed to read file "+f.Name())
			}
			f.Close()
		}
		sha1Sum = sum.Sum(sha1Sum[:0])
		if [sha1.Size]byte(sha1Sum[:sha1.Size]) == d.info.Pieces[pieceIndex] {
			d.bm.Set(pieceIndex)
			d.completed.Add(d.pieceLength(pieceIndex))
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
		for _, c := range d.pieceInfo.fileChunks(i) {
			ef, ok := efs[c.fileIndex]
			if !ok {
				shouldCheck = false
				break
			}

			if c.offsetOfFile > ef.size || c.offsetOfFile+c.length > ef.size {
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

		if doAlloc && selected {
			return nil, fallocate.Fallocate(f, 0, size)
		}

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

		return nil, errgo.Wrap(fallocate.Fallocate(f, fs, size-fs), "failed to alloc file")
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

		p := filepath.Join(d.basePath, tf.Path)
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

func (d *Download) check(resumed bool, skipHashCheck bool) {
	if !resumed {
		d.log.Debug().Msg("initializing download")
		d.state.Store(uint32(Checking))
	}

	if skipHashCheck {
		if err := d.verifyFileSizes(); err != nil {
			if resumed {
				d.log.Warn().Err(err).Msg("file size verification failed on resume")
			} else {
				d.setError(err)
				d.log.Err(err).Msg("file size verification failed")
			}
		} else if !resumed {
			d.bm.Fill()
		}
	} else if !resumed {
		if err := d.initCheck(); err != nil {
			d.setError(err)
			d.log.Err(err).Msg("failed to initCheck torrent data")
		}
	}

	if !resumed {
		// unsafe methods are safe here because d hasn't been shared with other goroutines yet.
		d.markUnselectedPiecesDoneUnsafe()
		d.completed.Store(d.computeCompletedUnsafe())
		d.ioDown.Reset()

		d.log.Debug().Msgf("done size %s", humanize.IBytes(uint64(d.bm.Count())*uint64(d.info.PieceLength)))

		if d.bm.Count() == d.info.NumPieces {
			if err := d.transition(Seeding); err != nil {
				d.log.Error().Err(err).Msg("failed to transition state after init check")
			}
		} else {
			if err := d.transition(Downloading); err != nil {
				d.log.Error().Err(err).Msg("failed to transition state after init check")
			}
		}
	}
}
