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

	"tyr/internal/meta"
	"tyr/internal/pkg/assert"
	"tyr/internal/pkg/fallocate"
	"tyr/internal/pkg/gfs"
	"tyr/internal/pkg/global"
)

type existingFile struct {
	index int
	size  int64
}

func (d *Download) initCheck() error {
	if global.Dev {
		d.bm.Fill()
		return nil
	}

	d.log.Debug().Msg("initCheck")
	if err := os.MkdirAll(d.basePath, os.ModePerm); err != nil {
		return err
	}

	d.log.Debug().Msg("try pre alloc")
	var efs = make(map[int]*existingFile, len(d.info.Files)+1)
	for i, tf := range d.info.Files {
		p := tf.Path
		f, e := tryAllocFile(i, filepath.Join(d.basePath, p), tf.Length, d.c.Config.App.Fallocate.Load())
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

	for _, pieceIndex := range h {
		select {
		case <-d.ctx.Done():
			return d.ctx.Err()
		default:
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
				return errgo.Wrap(err, fmt.Sprintf("failed to read file %s", f.File.Name()))
			}

			f.Release()
		}

		if [sha1.Size]byte(sum.Sum(nil)) == d.info.Pieces[pieceIndex] {
			d.bm.Set(pieceIndex)
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

	for i := uint32(0); i < d.info.NumPieces; i++ {
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

type pieceFileChunks struct {
	fileChunks []pieceInfoFileChunk
}

func buildPieceInfos(info meta.Info) []pieceFileChunks {
	p := make([]pieceFileChunks, info.NumPieces)

	for i := uint32(0); i < info.NumPieces; i++ {
		p[i] = getPieceInfo(i, info)
	}

	return p
}

func getPieceInfo(i uint32, info meta.Info) pieceFileChunks {
	assert.NotEqual(info.Pieces[i], [20]byte{})

	return pieceFileChunks{
		fileChunks: pieceFileInfos(i, info),
	}
}

type pieceInfoFileChunk struct {
	fileIndex    int
	offsetOfFile int64
	length       int64
}

func pieceFileInfos(i uint32, info meta.Info) []pieceInfoFileChunk {
	var pieceStart = int64(i) * info.PieceLength
	var currentFileStart int64 = 0
	var needToRead = info.PieceLength
	var fileIndex = 0

	var result []pieceInfoFileChunk

	for needToRead > 0 {
		f := info.Files[fileIndex]
		currentFileEnd := currentFileStart + f.Length
		currentReadStart := pieceStart + (info.PieceLength - needToRead)

		if currentFileStart <= currentReadStart && currentReadStart <= currentFileEnd {

			shouldRead := min(currentFileEnd-currentReadStart, needToRead)

			result = append(result, pieceInfoFileChunk{
				fileIndex:    fileIndex,
				offsetOfFile: currentReadStart - currentFileStart,
				length:       shouldRead,
			})

			needToRead = needToRead - shouldRead
		}

		currentFileStart += f.Length

		fileIndex++

		if fileIndex >= len(info.Files) {
			break
		}
	}

	if needToRead < 0 {
		panic("unexpected need to read")
	}

	return result
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
