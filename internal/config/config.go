// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

type Application struct {
	Crypto                   string `toml:"crypto"`
	DownloadDir              string `toml:"download-dir"`
	PiecePickStrategy        string `toml:"piece-pick-strategy"`
	GlobalDownloadSpeedLimit int64  `toml:"global-download-speed-limit"`
	MaxHTTPParallel          int    `toml:"max-http-parallel"`
	MaxRequestBodySize       int64  `toml:"max-rpc-request-body-size"`
	GlobalUploadSpeedLimit   int64  `toml:"global-upload-speed-limit"`
	P2PPort                  uint16 `toml:"p2p-port"`
	GlobalUploadSlots        uint16 `toml:"global-upload-slots"`
	GlobalConnectionLimit    uint16 `toml:"global-connections-limit"`
	NumWant                  uint16 `toml:"num-want"`
	Fallocate                bool   `toml:"fallocate"`
	RecheckOnComplete        bool   `toml:"recheck-on-complete"`
}

type Config struct {
	App Application `toml:"application"`
}
