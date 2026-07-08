// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package client

import (
	"encoding/json"
	"io"
	"runtime"

	"neptune/internal/download"
)

// DumpState writes a comprehensive JSON debug snapshot of the entire process.
// Designed for diagnosing hangs — collects state from all downloads with
// minimal lock holding.
func (c *Client) DumpState(w io.Writer) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	c.m.RLock()
	conns := len(c.downloadMap)
	dlRate := c.session.PieceDownloadRate.Status()
	ulRate := c.session.PieceUploadRate.Status()
	dlRateLimit := c.session.DownloadLimiter.Rate()
	ulRateLimit := c.session.UploadLimiter.Rate()
	uploadQLen := len(c.session.UploadQ)
	uploadQCap := cap(c.session.UploadQ)
	connCount := c.session.ConnCount.Load()

	// Snapshot download references under lock, then release.
	type downloadEntry struct {
		d   *download.Download
		idx int
	}
	var entries []downloadEntry
	for i, ih := range c.infoHashes {
		if d, ok := c.downloadMap[ih]; ok {
			entries = append(entries, downloadEntry{idx: i, d: d})
		}
	}
	checkQueueLen := len(c.checkQueue)
	c.m.RUnlock()

	// Build per-download debug data.
	downloads := make([]any, len(entries))
	for i, e := range entries {
		downloads[i] = download.BuildDebugPageData(e.d, e.d.InfoHash().Hex(), true)
	}

	var ipv4, ipv6 string
	if v4 := c.session.IPv4.Load(); v4 != nil {
		ipv4 = v4.String()
	}
	if v6 := c.session.IPv6.Load(); v6 != nil {
		ipv6 = v6.String()
	}

	resp := stateDump{
		Goroutines:    runtime.NumGoroutine(),
		HeapAlloc:     ms.HeapAlloc,
		HeapSys:       ms.HeapSys,
		HeapObjects:   ms.HeapObjects,
		StackInUse:    ms.StackInuse,
		NumGC:         ms.NumGC,
		Connections:   connCount,
		Torrents:      conns,
		CheckQueue:    checkQueueLen,
		DownloadRate:  dlRate.CurRate,
		DownloadTotal: dlRate.Total,
		UploadRate:    ulRate.CurRate,
		UploadTotal:   ulRate.Total,
		DownloadLimit: dlRateLimit,
		UploadLimit:   ulRateLimit,
		UploadQLen:    uploadQLen,
		UploadQCap:    uploadQCap,
		IPv4:          ipv4,
		IPv6:          ipv6,
		Downloads:     downloads,
	}

	_ = json.NewEncoder(w).Encode(resp)
}

type stateDump struct {
	IPv4          string `json:"ipv4"`
	IPv6          string `json:"ipv6"`
	Downloads     []any  `json:"downloads"`
	HeapSys       uint64 `json:"heap_sys"`
	StackInUse    uint64 `json:"stack_in_use"`
	UploadRate    int64  `json:"upload_rate"`
	UploadTotal   int64  `json:"upload_total"`
	DownloadLimit int64  `json:"download_limit"`
	UploadLimit   int64  `json:"upload_limit"`
	HeapAlloc     uint64 `json:"heap_alloc"`
	DownloadRate  int64  `json:"download_rate"`
	HeapObjects   uint64 `json:"heap_objects"`
	DownloadTotal int64  `json:"download_total"`
	Goroutines    int    `json:"goroutines"`
	UploadQCap    int    `json:"upload_queue_cap"`
	Torrents      int    `json:"torrents"`
	CheckQueue    int    `json:"check_queue"`
	UploadQLen    int    `json:"upload_queue_len"`
	Connections   uint32 `json:"connections"`
	NumGC         uint32 `json:"num_gc"`
}
