// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"neptune/internal/pkg/null"
)

func writeConfig(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	return path
}

// TestLuaConfigKeys pins the Lua key surface. Adding a config field changes this
// list, so the change has to be deliberate.
func TestLuaConfigKeys(t *testing.T) {
	want := []string{
		"application.connection-speed",
		"application.crypto",
		"application.download-dir",
		"application.download-slots",
		"application.fallocate",
		"application.global-connections-limit",
		"application.global-download-speed-limit",
		"application.global-upload-slots",
		"application.global-upload-speed-limit",
		"application.hook.on-download-completed",
		"application.hook.on-download-started",
		"application.hook.timeout",
		"application.max-http-parallel",
		"application.max-rpc-request-body-size",
		"application.num-want",
		"application.p2p-port",
		"application.piece-pick-strategy",
		"application.recheck-on-complete",
		"application.slow-download-speed-threshold",
		"application.torrent-connection-limit",
	}

	assert.Equal(t, want, validConfigKeys())
}

// TestLuaConfigRoundTrip writes and reads back every key through the Lua API, so
// a field whose converter or getter does not match its type fails here.
func TestLuaConfigRoundTrip(t *testing.T) {
	samples := map[reflect.Type]string{
		reflect.TypeFor[string](): `"sample"`,
		reflect.TypeFor[bool]():   `true`,
		reflect.TypeFor[int]():    `7`,
		reflect.TypeFor[int64]():  `7`,
		reflect.TypeFor[uint16](): `7`,
		durationType:              `"1m30s"`,
	}

	// Fields that constrain their value need a value validation accepts.
	explicit := map[string]string{
		"application.crypto":              `"force"`,
		"application.piece-pick-strategy": `"sequential"`,
	}

	var script strings.Builder

	for _, key := range validConfigKeys() {
		typ := configFieldType(t, key)

		sample, ok := explicit[key]
		if !ok {
			sample, ok = samples[typ]
		}
		require.Truef(t, ok, "no sample value for %s of type %s", key, typ)

		fmt.Fprintf(&script, "neptune.set(%q, %s)\nassert(neptune.get(%q) == %s, %q)\n", key, sample, key, sample, key)
	}

	_, err := Load(writeConfig(t, "config.lua", script.String()), Overrides{})
	require.NoError(t, err)
}

func configFieldType(t *testing.T, key string) reflect.Type {
	t.Helper()

	index, ok := configLeafFields[key]
	require.Truef(t, ok, "unknown config key %s", key)

	var cfg Config

	return reflect.ValueOf(&cfg).Elem().FieldByIndex(index).Type()
}

// TestLoadLuaRecheckOnComplete is a regression test: the key was reachable from
// TOML but missing from the hand-written Lua key table.
func TestLoadLuaRecheckOnComplete(t *testing.T) {
	script := writeConfig(t, "config.lua", `neptune.set("application.recheck-on-complete", true)`)

	cfg, err := Load(script, Overrides{})
	require.NoError(t, err)
	assert.True(t, cfg.App.RecheckOnComplete)
}

func TestLoadTOML(t *testing.T) {
	path := writeConfig(t, "config.toml", `
[application]
p2p-port = 12345
recheck-on-complete = true
piece-pick-strategy = "sequential"
crypto = "force"
`)

	cfg, err := Load(path, Overrides{})
	require.NoError(t, err)
	assert.Equal(t, uint16(12345), cfg.App.P2PPort)
	assert.True(t, cfg.App.RecheckOnComplete)
	assert.Equal(t, "sequential", cfg.App.PiecePickStrategy)
	assert.Equal(t, "force", cfg.App.Crypto)
}

func TestLoadEmptyPathUsesDefaults(t *testing.T) {
	cfg, err := Load("", Overrides{})
	require.NoError(t, err)
	assert.Equal(t, uint16(50047), cfg.App.P2PPort)
	assert.Equal(t, 100, cfg.App.MaxHTTPParallel)
	assert.NotEmpty(t, cfg.App.DownloadDir)
}

// TestLoadMissingFile: a path was given, so the file has to be there. The
// defaults only apply when no path was given at all.
func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "config.toml"), Overrides{})
	require.Error(t, err)
}

func TestLoadUnknownFormat(t *testing.T) {
	_, err := Load(writeConfig(t, "config.yaml", ""), Overrides{})
	require.ErrorContains(t, err, "unknown config format")
}

func TestLoadOverrideP2PPort(t *testing.T) {
	path := writeConfig(t, "config.toml", "[application]\np2p-port = 12345\n")

	cfg, err := Load(path, Overrides{App: AppOverrides{P2PPort: null.New(uint16(999))}})
	require.NoError(t, err)
	assert.Equal(t, uint16(999), cfg.App.P2PPort)
}

// TestLoadUnsetOverrideKeepsFileValue is a regression test: the flag used to be
// copied into the config unconditionally, so the file value never survived.
func TestLoadUnsetOverrideKeepsFileValue(t *testing.T) {
	path := writeConfig(t, "config.toml", "[application]\np2p-port = 12345\n")

	cfg, err := Load(path, Overrides{})
	require.NoError(t, err)
	assert.Equal(t, uint16(12345), cfg.App.P2PPort)
}

// TestLoadRejectsInvalidValues: validation happens once, after every source has
// been applied, so both formats reject the same values.
func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
	}{
		{"toml crypto", "config.toml", "[application]\ncrypto = \"bogus\"\n"},
		{"lua crypto", "config.lua", `neptune.set("application.crypto", "bogus")`},
		{"toml piece pick strategy", "config.toml", "[application]\npiece-pick-strategy = \"typo\"\n"},
		{"lua piece pick strategy", "config.lua", `neptune.set("application.piece-pick-strategy", "typo")`},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.file, tt.content), Overrides{})
			require.ErrorContains(t, err, "invalid config")
		})
	}
}

// TestLoadAcceptsEmptyOptionals: leaving an optional key empty means "use the
// default", which is not a validation error.
func TestLoadAcceptsEmptyOptionals(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
	}{
		{"toml", "config.toml", "[application]\ncrypto = \"\"\npiece-pick-strategy = \"\"\n"},
		{"lua", "config.lua", "neptune.set(\"application.crypto\", \"\")\nneptune.set(\"application.piece-pick-strategy\", \"\")"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.file, tt.content), Overrides{})
			require.NoError(t, err)
		})
	}
}

// TestLoadAcceptsValidValues pins the accepted value sets, which the docs
// promise are the same for both formats.
func TestLoadAcceptsValidValues(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"crypto prefer", "[application]\ncrypto = \"prefer\"\n"},
		{"crypto force", "[application]\ncrypto = \"force\"\n"},
		{"crypto prefer-no-encryption", "[application]\ncrypto = \"prefer-no-encryption\"\n"},
		{"crypto none", "[application]\ncrypto = \"none\"\n"},
		{"piece pick strategy rarest-first", "[application]\npiece-pick-strategy = \"rarest-first\"\n"},
		{"piece pick strategy sequential", "[application]\npiece-pick-strategy = \"sequential\"\n"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, "config.toml", tt.content), Overrides{})
			require.NoError(t, err)
		})
	}
}

func TestLoadLuaUnknownKeyListsSortedKeys(t *testing.T) {
	script := writeConfig(t, "config.lua", `neptune.set("application.nope", 1)`)

	_, err := Load(script, Overrides{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
	assert.Contains(t, err.Error(), "application.connection-speed, application.crypto,")
}
