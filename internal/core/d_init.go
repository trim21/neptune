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
	"github.com/juju/ratelimit"
	"github.com/trim21/errgo"

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
		f, e := tryAllocFile(i, filepath.Join(d.basePath, p), tf.Length, d.c.Config.App.Fallocate)
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

		piece := d.pieceInfo[pieceIndex]
		for _, chunk := range piece.fileChunks {
			f, err := d.openFile(chunk.fileIndex)
			if err != nil {
				return errgo.Wrap(err, fmt.Sprintf("failed to open file %q", filepath.Join(d.basePath, d.info.Files[chunk.fileIndex].Path)))
			}

			_, err = d.ioDown.IO64(gfs.CopyReaderAt(w, f.File, chunk.offsetOfFile, chunk.length))
			if err != nil {
				f.Close()
				return errgo.Wrap(err, "failed to read file "+f.File.Name())
			}

			f.Release()
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
		p := d.pieceInfo[i]
		shouldCheck := true
		for _, c := range p.fileChunks {
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

func tryAllocFile(index int, path string, size int64, doAlloc bool) (*existingFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}

		if !doAlloc {
			return nil, nil
		}

		if err = os.MkdirAll(filepath.Dir(path), os.ModePerm); err != nil {
			return nil, err
		}

		f, err = os.Create(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return nil, fallocate.Fallocate(f, 0, size)
	}

	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var ef *existingFile
	fs := stat.Size()
	if fs != 0 {
		ef = &existingFile{index: index, size: fs}
	}

	if doAlloc {
		if fs != size {
			return nil, errgo.Wrap(fallocate.Fallocate(f, fs, size-fs), "failed to alloc file")
		}
	}

	return ef, nil
}
