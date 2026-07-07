// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"maps"
	"slices"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/gslice"
)

// AddTags adds tags to the download and persists.
func (d *Download) AddTags(tags []string) {
	d.s.mu.Lock()
	for _, tag := range tags {
		if !slices.Contains(d.s.tags, tag) {
			d.s.tags = append(d.s.tags, tag)
		}
	}
	d.s.mu.Unlock()
	d.saveResume()
}

// RemoveTag removes a tag from the download and persists.
func (d *Download) RemoveTag(tag string) {
	d.s.mu.Lock()
	d.s.tags = gslice.Remove(d.s.tags, tag)
	d.s.mu.Unlock()
	d.saveResume()
}

// SetCustom sets a custom key-value pair and persists.
func (d *Download) SetCustom(key, value string) {
	d.s.mu.Lock()
	d.s.custom[key] = value
	d.s.mu.Unlock()
	d.saveResume()
}

// UpdateCustom replaces all custom key-value pairs and persists.
func (d *Download) UpdateCustom(c map[string]string) {
	d.s.mu.Lock()
	maps.Copy(d.s.custom, c)
	d.s.mu.Unlock()
	d.saveResume()
}

// DelCustom removes a custom key and persists.
func (d *Download) DelCustom(key string) {
	d.s.mu.Lock()
	delete(d.s.custom, key)
	d.s.mu.Unlock()
	d.saveResume()
}

// SetDownloadLimit updates the per-torrent download rate limit and persists.
func (d *Download) SetDownloadLimit(limit int64) {
	d.downloadLimiter.Update(limit)
	d.saveResume()
}

// SetUploadLimit updates the per-torrent upload rate limit and persists.
func (d *Download) SetUploadLimit(limit int64) {
	d.uploadLimiter.Update(limit)
	d.saveResume()
}

// SaveResume persists the current download state to the resume file.
func (d *Download) SaveResume() {
	d.saveResume()
}

// Cancel cancels the download context.
func (d *Download) Cancel() {
	d.cancel()
}

// BackgroundWgWait waits for all background goroutines to finish.
func (d *Download) BackgroundWgWait() {
	d.backgroundWg.Wait()
}

// ResumeFilePath returns the directory and filename for the resume file.
func (d *Download) ResumeFilePath() (dir, file string) {
	return d.resumeFilePath()
}

// InfoHashHex returns the torrent info hash as a hex string.
func (d *Download) InfoHashHex() string {
	return d.info.Hash.Hex()
}

// InfoHashBytes returns the torrent info hash as a byte slice.
func (d *Download) InfoHashBytes() []byte {
	return d.info.Hash[:]
}

// InfoHash returns the torrent info hash.
func (d *Download) InfoHash() metainfo.Hash {
	return d.info.Hash
}

// BasePath returns the base filesystem path where data is stored.
func (d *Download) BasePath() string {
	d.s.mu.RLock()
	defer d.s.mu.RUnlock()
	return d.s.basePath
}

// DataFiles returns file paths relative to BasePath for cleanup operations.
func (d *Download) DataFiles() []string {
	result := make([]string, len(d.info.Files))
	for i, f := range d.info.Files {
		result[i] = f.Path
	}
	return result
}
