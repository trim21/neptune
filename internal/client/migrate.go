// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/trim21/go-bencode"

	"neptune/internal/session/store"
)

// migrateLegacyResume imports legacy per-torrent .resume files into the
// session store, then removes them. Runs once at startup; after migration no
// .resume files remain, so later starts skip it.
func (c *Client) migrateLegacyResume() error {
	resumeDir := filepath.Join(c.session.SessionPath, "resume")

	var files []string
	if err := filepath.Walk(resumeDir, func(path string, info fs.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.HasSuffix(path, ".resume") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return err
	}

	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn().Err(err).Str("path", path).Msg("failed to read legacy resume file, skipping")
			continue
		}

		var r store.Resume
		if err := bencode.Unmarshal(data, &r); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("failed to decode legacy resume file, skipping")
			continue
		}

		if err := c.session.Store.Upsert(&r); err != nil {
			return err
		}

		if err := os.Remove(path); err != nil {
			log.Warn().Err(err).Str("path", path).Msg("failed to remove migrated resume file")
		}
	}

	// Prune the legacy layout {resume}/{xx}/{hash}.resume: remove the now-empty
	// {xx} subdirectories, then the resume root. Non-empty directories are left
	// alone (os.Remove fails on them) so unrelated files are never touched.
	var dirs []string
	if err := filepath.Walk(resumeDir, func(path string, info fs.FileInfo, _ error) error {
		if info != nil && info.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, dir := range slices.Backward(dirs) {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			log.Warn().Err(err).Str("path", dir).Msg("failed to remove empty resume directory")
		}
	}

	return nil
}
