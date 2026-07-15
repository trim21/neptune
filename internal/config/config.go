// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"time"
)

// CryptoMode controls MSE encryption policy.
type CryptoMode uint8

const (
	CryptoPrefer             CryptoMode = iota // prefer RC4, fallback to plain TCP (default)
	CryptoForce                                // require RC4, drop connection on MSE failure
	CryptoPreferNoEncryption                   // prefer plaintext, fallback to plain TCP
	CryptoNone                                 // disable MSE entirely
)

// ParseCryptoMode converts a config string to CryptoMode.
func ParseCryptoMode(s string) (CryptoMode, error) {
	switch s {
	case "", "prefer":
		return CryptoPrefer, nil
	case "force":
		return CryptoForce, nil
	case "prefer-no-encryption":
		return CryptoPreferNoEncryption, nil
	case "none":
		return CryptoNone, nil
	default:
		return 0, fmt.Errorf("invalid crypto mode %q: must be 'force', 'prefer', 'prefer-no-encryption', or 'none'", s)
	}
}

// HookConfig holds shell commands to run on download events.
// Commands run via /bin/sh -c with environment variables:
//
//	NEPTUNE_INFO_HASH  — hex-encoded info hash
//	NEPTUNE_NAME       — torrent name
//	NEPTUNE_SAVE_PATH  — download directory
//	NEPTUNE_SIZE       — total size in bytes
//
// Empty strings mean no hook (no-op).
type HookConfig struct {
	OnDownloadStarted   string        `toml:"on-download-started"`
	OnDownloadCompleted string        `toml:"on-download-completed"`
	Timeout             time.Duration `toml:"timeout"`
}

type Application struct {
	DownloadDir                string     `toml:"download-dir"`
	PiecePickStrategy          string     `toml:"piece-pick-strategy"`
	Crypto                     string     `toml:"crypto"`
	Hook                       HookConfig `toml:"hook"`
	SlowDownloadSpeedThreshold int64      `toml:"slow-download-speed-threshold"`
	GlobalUploadSpeedLimit     int64      `toml:"global-upload-speed-limit"`
	MaxRequestBodySize         int64      `toml:"max-rpc-request-body-size"`
	MaxHTTPParallel            int        `toml:"max-http-parallel"`
	GlobalDownloadSpeedLimit   int64      `toml:"global-download-speed-limit"`
	P2PPort                    uint16     `toml:"p2p-port"`
	GlobalConnectionLimit      uint16     `toml:"global-connections-limit"`
	TorrentConnectionLimit     uint16     `toml:"torrent-connection-limit"`
	DownloadSlots              uint16     `toml:"download-slots"`
	GlobalUploadSlots          uint16     `toml:"global-upload-slots"`
	NumWant                    uint16     `toml:"num-want"`
	Fallocate                  bool       `toml:"fallocate"`
	RecheckOnComplete          bool       `toml:"recheck-on-complete"`
}

type Config struct {
	App Application `toml:"application"`
}
