// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
	"github.com/trim21/errgo"

	"neptune/internal/pkg/null"
)

// DefaultConfig returns the built-in defaults used when a setting is not
// provided by the config file or by an override.
func DefaultConfig() Config {
	return Config{
		App: Application{
			P2PPort:                50047,
			MaxHTTPParallel:        100,
			GlobalConnectionLimit:  200,
			TorrentConnectionLimit: 50,
			ConnectionSpeed:        30,
			MaxRequestBodySize:     50 << 20,
		},
	}
}

// Overrides holds values that win over the config file, typically from command
// line flags and environment variables. A value that was not provided is left
// unset.
type Overrides struct {
	App AppOverrides
}

type AppOverrides struct {
	P2PPort null.Null[uint16]
}

// Load builds the final config: it applies the built-in defaults, the config
// file at path, the explicit overrides and the derived defaults, in that
// order, and validates the result. An empty path means there is no config
// file.
//
// Load is the only way to obtain a Config, so every caller gets a validated
// one no matter which source a value came from.
func Load(path string, overrides Overrides) (Config, error) {
	cfg := DefaultConfig()

	if path != "" {
		var err error

		switch filepath.Ext(path) {
		case ".lua":
			cfg, err = loadLua(path, cfg)
		case ".toml":
			cfg, err = loadTOML(path, cfg)
		default:
			return Config{}, fmt.Errorf("unknown config format %q, expected .toml or .lua", path)
		}

		if err != nil {
			return Config{}, err
		}
	}

	if overrides.App.P2PPort.Set {
		cfg.App.P2PPort = overrides.App.P2PPort.Value
	}

	applyDefaults(&cfg.App)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func loadTOML(path string, cfg Config) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, errgo.Wrap(err, "open config file")
	}
	defer f.Close()

	if err := toml.NewDecoder(f).DisallowUnknownFields().Decode(&cfg); err != nil {
		return Config{}, errgo.Wrap(err, "failed to parse config file")
	}

	return cfg, nil
}

func applyDefaults(app *Application) {
	if app.DownloadDir == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			panic(errgo.Wrap(err, "failed to get user homedir"))
		}
		app.DownloadDir = filepath.Join(hd, "downloads")
	}

	if app.GlobalUploadSlots == 0 {
		slots := max(app.GlobalConnectionLimit*4, 64)
		app.GlobalUploadSlots = slots
	}
}
