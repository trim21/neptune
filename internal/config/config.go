// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import "fmt"

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
