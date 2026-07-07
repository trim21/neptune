// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

// FileChunkInfo describes a contiguous byte range within a single file.
type FileChunkInfo struct {
	FileIndex    int
	OffsetOfFile int64
	Length       int64
}

// PieceInfo maps piece indices to their file chunk layout. It is precomputed
// once per torrent and provides O(1) zero-alloc chunk lookups.
type PieceInfo struct {
	offsets []uint32        // NumPieces+1 entries, piece i uses chunks[offsets[i]:offsets[i+1]]
	chunks  []FileChunkInfo // single contiguous array
}

// FileChunks returns the file chunks for the given piece index.
func (p *PieceInfo) FileChunks(index uint32) []FileChunkInfo {
	return p.chunks[p.offsets[index]:p.offsets[index+1]]
}

// BuildPieceInfos precomputes file chunk layouts for all pieces.
func BuildPieceInfos(info Info) PieceInfo {
	offsets := make([]uint32, info.NumPieces+1)
	chunks := make([]FileChunkInfo, 0, info.NumPieces)

	for i := range info.NumPieces {
		offsets[i] = uint32(len(chunks))
		chunks = append(chunks, pieceFileChunks(i, info)...)
	}

	offsets[info.NumPieces] = uint32(len(chunks))
	return PieceInfo{offsets: offsets, chunks: chunks}
}

func pieceFileChunks(i uint32, info Info) []FileChunkInfo {
	return FileChunks(info, int64(i)*info.PieceLength, min(int64(i+1)*info.PieceLength, info.TotalLength))
}

// FileChunks returns the chunk layout for the given torrent byte range.
func FileChunks(info Info, pieceStart, end int64) []FileChunkInfo {
	var currentFileStart int64
	var needToRead = end - pieceStart
	var fileIndex int

	var result []FileChunkInfo

	for needToRead > 0 {
		f := info.Files[fileIndex]
		currentFileEnd := currentFileStart + f.Length
		currentReadStart := end - needToRead

		if currentFileStart <= currentReadStart && currentReadStart <= currentFileEnd {
			shouldRead := min(currentFileEnd-currentReadStart, needToRead)

			result = append(result, FileChunkInfo{
				FileIndex:    fileIndex,
				OffsetOfFile: currentReadStart - currentFileStart,
				Length:       shouldRead,
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
