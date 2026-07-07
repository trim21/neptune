// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

// FileChunkInfo describes a contiguous byte range within a single file.
// Kept for backward compatibility; prefer info.ForEachFileChunk for zero-alloc lookups.
type FileChunkInfo struct {
	FileIndex    int
	OffsetOfFile int64
	Length       int64
}
