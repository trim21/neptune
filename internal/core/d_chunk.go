// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"neptune/internal/meta"
	"neptune/internal/pkg/assert"
)

func (d *Download) pieceLength(index uint32) int64 {
	if index == d.info.NumPieces-1 {
		return d.info.LastPieceSize
	}

	return d.info.PieceLength
}

type pieceFileChunks struct {
	fileChunks []fileChunkInfo
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

type fileChunkInfo struct {
	fileIndex    int
	offsetOfFile int64
	length       int64
}

func pieceFileInfos(i uint32, info meta.Info) []fileChunkInfo {
	return fileChunks(info, int64(i)*info.PieceLength, min(int64(i+1)*info.PieceLength, info.TotalLength))
}

func fileChunks(info meta.Info, pieceStart, end int64) []fileChunkInfo {
	var currentFileStart int64 = 0
	var needToRead = end - pieceStart
	var fileIndex = 0

	var result []fileChunkInfo

	for needToRead > 0 {
		f := info.Files[fileIndex]
		currentFileEnd := currentFileStart + f.Length
		currentReadStart := end - needToRead

		if currentFileStart <= currentReadStart && currentReadStart <= currentFileEnd {

			shouldRead := min(currentFileEnd-currentReadStart, needToRead)

			result = append(result, fileChunkInfo{
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
