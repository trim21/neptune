// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFromLua_SimpleSet(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		neptune.set("application.p2p-port", 12345)
		neptune.set("application.fallocate", true)
		neptune.set("application.download-dir", "/custom/downloads")
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, uint16(12345), cfg.App.P2PPort)
	assert.True(t, cfg.App.Fallocate)
	assert.Equal(t, "/custom/downloads", cfg.App.DownloadDir)
}

func TestLoadFromLua_Defaults(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		print("hello")
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.App.MaxHTTPParallel)
	assert.Equal(t, uint16(50), cfg.App.GlobalConnectionLimit)
	assert.False(t, cfg.App.Fallocate)
}

func TestLoadFromLua_GetAndSet(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		local conns = neptune.get("application.global-connections-limit")
		neptune.set("application.global-connections-limit", math.max(conns, 100))
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, uint16(100), cfg.App.GlobalConnectionLimit)
}

func TestLoadFromLua_LastSetWins(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		neptune.set("application.global-upload-speed-limit", 200 * 1024 * 1024)
		neptune.set("application.global-upload-speed-limit", 10 * 1024 * 1024)
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, int64(10*1024*1024), cfg.App.GlobalUploadSpeedLimit)
}

func TestLoadFromLua_OsEnv(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		if os.getenv("NODE_NAME") == "testnode" then
			neptune.set("application.download-dir", "/mnt/test/downloads")
			neptune.set("application.global-connections-limit", 200)
		else
			neptune.set("application.global-connections-limit", 50)
		end
	`), 0644))

	t.Setenv("NODE_NAME", "testnode")

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, "/mnt/test/downloads", cfg.App.DownloadDir)
	assert.Equal(t, uint16(200), cfg.App.GlobalConnectionLimit)
}

func TestLoadFromLua_OsEnvNotSet(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		local node = os.getenv("NONEXISTENT")
		neptune.set("application.download-dir", "/data/" .. tostring(node))
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.Equal(t, "/data/", cfg.App.DownloadDir)
}

func TestLoadFromLua_OsHostname(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		local host = os.hostname()
		neptune.set("application.download-dir", "/data/" .. host)
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)

	expectedHost, _ := os.Hostname()
	assert.Equal(t, "/data/"+expectedHost, cfg.App.DownloadDir)
}

func TestLoadFromLua_OsCpus(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		neptune.set("application.global-connections-limit", os.cpus() * 20)
	`), 0644))

	cfg, err := LoadFromLua(script)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, cfg.App.GlobalConnectionLimit, uint16(20))
}

func TestLoadFromLua_InvalidKey(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		neptune.set("nonexistentKey", 123)
	`), 0644))

	_, err := LoadFromLua(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestLoadFromLua_InvalidValueType(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`
		neptune.set("application.p2p-port", "not a number")
	`), 0644))

	_, err := LoadFromLua(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
}

func TestLoadFromLua_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "config.lua")
	require.NoError(t, os.WriteFile(script, []byte(`invalid lua syntax {{{`), 0644))

	_, err := LoadFromLua(script)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "execute config script")
}
