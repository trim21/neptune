// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/trim21/errgo"
	"go.uber.org/atomic"
)

type Application struct {
	DownloadDir     string `toml:"download-dir"`
	MaxHTTPParallel int    `toml:"max-http-parallel"`
	P2PPort         uint16 `toml:"p2p-port"`
	NumWant         uint16 `toml:"num-want"`
	// hard global connection limit
	GlobalConnectionLimit uint16      `toml:"global-connections-limit"`
	Fallocate             atomic.Bool `toml:"fallocate"`
}

type Config struct {
	App Application `toml:"application"`
}

func LoadFromFile(path string) (Config, error) {
	var cfg = Config{
		App: Application{MaxHTTPParallel: 100, GlobalConnectionLimit: 50},
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return Config{}, errgo.Wrap(err, "failed to read config file")
	}
	defer f.Close()

	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(&cfg); err != nil {
		return cfg, errgo.Wrap(err, "failed to parse config file")
	}

	if cfg.App.DownloadDir == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			panic(errgo.Wrap(err, "failed to get user homedir"))
		}

		cfg.App.DownloadDir = filepath.Join(hd, "downloads")
	}

	return cfg, nil
}
