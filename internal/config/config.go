// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

type Application struct {
	DownloadDir string `toml:"download-dir"`

	Crypto string `toml:"crypto"`

	MaxHTTPParallel int `toml:"max-http-parallel"`

	P2PPort uint16 `toml:"p2p-port"`

	NumWant uint16 `toml:"num-want"`

	// hard global connection limit
	GlobalConnectionLimit uint16 `toml:"global-connections-limit"`

	// hard global upload slot limit (across all torrents)
	// 0 means auto (derived from GlobalConnectionLimit).
	GlobalUploadSlots uint16 `toml:"global-upload-slots"`

	// Global download speed limit in bytes per second. 0 means unlimited.
	GlobalDownloadSpeedLimit int64 `toml:"global-download-speed-limit"`

	// Global upload speed limit in bytes per second. 0 means unlimited.
	GlobalUploadSpeedLimit int64 `toml:"global-upload-speed-limit"`

	Fallocate bool `toml:"fallocate"`

	// RecheckOnComplete enables automatic hash re-check after all pieces are
	// downloaded. If any piece fails verification the torrent goes back to
	// downloading to repair the corrupted blocks.
	RecheckOnComplete bool `toml:"recheck-on-complete"`

	// MaxRequestBodySize limits JSON-RPC request body size in bytes. 0 means no limit.
	MaxRequestBodySize int64 `toml:"max-rpc-request-body-size"`
}

type Config struct {
	App Application `toml:"application"`
}
