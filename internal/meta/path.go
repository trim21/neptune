// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package meta

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// nameMax is the maximum length of a single path component (file/directory name)
// on common file systems (ext4, NTFS, APFS, HFS+).
const nameMax = 255

// SafePathComponent ensures a single path component fits within the file system
// name length limit (nameMax bytes). If the name is too long, it is truncated and
// a short hash suffix is appended to preserve uniqueness.
//
// The function preserves the file extension when present.
func SafePathComponent(name string) string {
	if len(name) <= nameMax {
		return name
	}

	// Separate extension
	ext := filepath.Ext(name)
	nameBody := name[:len(name)-len(ext)]

	// Compute a short hash of the original name for uniqueness
	h := sha1.Sum([]byte(name))
	hashSuffix := "." + hex.EncodeToString(h[:3]) // 6 hex chars + leading dot = 7 chars

	// Reserve space for: hashSuffix + extension
	reserved := len(hashSuffix) + len(ext)

	// If extension itself is too long, fall back to simple truncation
	if reserved >= nameMax {
		return truncateToValidUTF8(name, nameMax)
	}

	truncateLen := nameMax - reserved
	if truncateLen <= 0 {
		return truncateToValidUTF8(name, nameMax)
	}

	truncated := truncateToValidUTF8(nameBody, truncateLen)
	return truncated + hashSuffix + ext
}

// truncateToValidUTF8 truncates s to at most maxLen bytes without splitting a
// multi-byte UTF-8 rune. If the truncation point falls inside a multi-byte rune,
// the rune is dropped entirely.
func truncateToValidUTF8(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Walk backward from maxLen to find a valid UTF-8 boundary.
	// A valid boundary is either the start of the string or a byte that starts a new rune
	// (i.e., not a continuation byte 0x80-0xBF).
	end := maxLen
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// RestoreFilePaths overwrites the Path and RawPath of each file with the
// persisted paths from resume data. This ensures that files can still be located
// even if the truncation algorithm changes between versions.
func RestoreFilePaths(files []File, persistedPaths []string) {
	for i, p := range persistedPaths {
		if i >= len(files) {
			break
		}
		files[i].Path = p
		files[i].RawPath = splitFilePath(p)
	}
}

// splitFilePath splits a file path into its components, handling the root path case.
func splitFilePath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, string(filepath.Separator))
}
