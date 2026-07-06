// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"strconv"
	"strings"
)

type Application struct {
	DownloadDir              string `toml:"download-dir"`
	P2PPort                  string `toml:"p2p-port"`
	MaxHTTPParallel          int    `toml:"max-http-parallel"`
	GlobalDownloadSpeedLimit int64  `toml:"global-download-speed-limit"`
	GlobalUploadSpeedLimit   int64  `toml:"global-upload-speed-limit"`
	NumWant                  uint16 `toml:"num-want"`
	GlobalConnectionLimit    uint16 `toml:"global-connections-limit"`
	GlobalUploadSlots        uint16 `toml:"global-upload-slots"`
	Fallocate                bool   `toml:"fallocate"`
}

type Config struct {
	App Application `toml:"application"`
}

// ValidateP2PPort checks that s is a valid port ("50047") or port range ("50047-50100").
func ValidateP2PPort(s string) (uint16, uint16, error) {
	parts := strings.SplitN(s, "-", 2)

	start, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid p2p port %q: %w", s, err)
	}

	if len(parts) == 1 {
		return uint16(start), uint16(start), nil
	}

	end, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid p2p port range %q: %w", s, err)
	}

	if end <= start {
		return 0, 0, fmt.Errorf("invalid p2p port range %q: end must be > start", s)
	}

	return uint16(start), uint16(end), nil
}
