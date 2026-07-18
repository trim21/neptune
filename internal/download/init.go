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

// DataInitResult carries the data-plane initialization result.
// All fields are required. Ownership of CompletedBm transfers to Download.
type DataInitResult struct {
	CompletedBm  *bm.Bitmap
	Completed    int64
	InitialState State // Downloading or Seeding (not Stopped)
}

// CheckExistingFiles pre-allocates files, hash-checks existing data, and
// returns a bitmap of verified pieces. selectedFilesSet determines which
// files are considered for the download.
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

// validateResumeBitfield checks that pieces marked in completedBm still have
// their files on disk. Pieces whose data is missing or truncated are cleared.
// Returns the adjusted completed byte count.
func validateResumeBitfield(info meta.Info, basePath string, selected *bm.Bitmap, completedBm *bm.Bitmap) (invalidBytes int64, err error) {
	var efs = make(map[int]*existingFile, len(info.Files)+1)
	for i, tf := range info.Files {
		if !selected.Contains(uint32(i)) {
			continue
		}
		p := filepath.Join(basePath, tf.Path)
		stat, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, errgo.Wrap(err, fmt.Sprintf("failed to stat %q", tf.Path))
		}
		if stat.Size() > 0 {
			efs[i] = &existingFile{index: i, size: stat.Size()}
		}
	}

	for i := range info.NumPieces {
		if !completedBm.Contains(i) {
			continue
		}
		valid := true
		for chunk := range info.PieceFileChunks(i) {
			ef, ok := efs[chunk.FileIndex]
			if !ok || chunk.OffsetOfFile+chunk.Length > ef.size {
				valid = false
				break
			}
		}
		if !valid {
			completedBm.Unset(i)
			invalidBytes += info.PieceLen(i)
		}
	}
	return invalidBytes, nil
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

// NewTorrentDataResult performs data-plane initialization for a brand new torrent:
// file pre-allocation, optional hash check, and bitmap construction.
func NewTorrentDataResult(ctx context.Context, info meta.Info, basePath string, selected *bm.Bitmap, fallocate bool, skipHashCheck bool) (DataInitResult, error) {
	var completedBm *bm.Bitmap

	if skipHashCheck {
		if err := verifyFileSizesStandalone(info, basePath, selected); err != nil {
			return DataInitResult{}, err
		}
		completedBm = bm.New(info.NumPieces)
		completedBm.Fill()
	} else {
		var err error
		completedBm, err = CheckExistingFiles(ctx, info, basePath, selected, fallocate)
		if err != nil {
			return DataInitResult{}, err
		}
	}

	completed := computeCompleted(info, completedBm, selected)

	state := Downloading
	if isInfoComplete(info, completedBm, selected) {
		state = Seeding
	}

	return DataInitResult{
		CompletedBm:  completedBm,
		Completed:    completed,
		InitialState: state,
	}, nil
}

// isInfoComplete returns true if all wanted pieces are in completedBm.
func isInfoComplete(info meta.Info, completedBm *bm.Bitmap, selected *bm.Bitmap) bool {
	if selected.Count() == uint32(len(info.Files)) {
		return completedBm.Count() == info.NumPieces
	}
	for i := range info.NumPieces {
		for chunk := range info.PieceFileChunks(i) {
			if selected.Contains(uint32(chunk.FileIndex)) {
				if !completedBm.Contains(i) {
					return false
				}
				break
			}
		}
	}
	return true
}

// computeCompleted counts total completed bytes from completedBm, restricted to
// pieces overlapping with wantedBm. wantedBm is derived from selected.
func computeCompleted(info meta.Info, completedBm *bm.Bitmap, selected *bm.Bitmap) int64 {
	wantedBm := buildWantedBm(info, selected)
	done := int64(completedBm.WithAnd(wantedBm).Count()) * info.PieceLength
	if completedBm.Contains(info.NumPieces-1) && wantedBm.Contains(info.NumPieces-1) {
		done = done - info.PieceLength + info.LastPieceSize
	}
	return done
}

// buildWantedBm builds a bitmap of pieces overlapping with selected files.
func buildWantedBm(info meta.Info, selected *bm.Bitmap) *bm.Bitmap {
	wanted := bm.New(info.NumPieces)
	if selected.Count() == uint32(len(info.Files)) {
		wanted.Fill()
		return wanted
	}
	for i := range info.NumPieces {
		for chunk := range info.PieceFileChunks(i) {
			if selected.Contains(uint32(chunk.FileIndex)) {
				wanted.Set(i)
				break
			}
		}
	}
	return wanted
}

// EmptyDataResult returns a zero DataInitResult suitable for tests that
// construct a Download directly without any on-disk data.
func EmptyDataResult(info meta.Info) DataInitResult {
	return DataInitResult{
		CompletedBm:  bm.New(info.NumPieces),
		Completed:    0,
		InitialState: Downloading,
	}
}

// SelectedFilesBitmap converts a list of file indices into a bitmap.
// An empty or nil slice means all files selected.
func SelectedFilesBitmap(info meta.Info, selectedFiles []int) *bm.Bitmap {
	s := bm.New(uint32(len(info.Files)))
	s.Fill()
	if len(selectedFiles) > 0 && len(selectedFiles) < len(info.Files) {
		s.Clear()
		for _, idx := range selectedFiles {
			s.Set(uint32(idx))
		}
	}
	return s
}

// initCheck runs hash verification and updates completedBm in-place.
// Used by runHashCheck for re-check after download completion.
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
