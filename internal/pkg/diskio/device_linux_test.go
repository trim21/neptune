// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package diskio

import (
	"path/filepath"
	"testing"
)

func TestParseDeviceID(t *testing.T) {
	id, ok := parseDeviceID("259:12")
	if !ok || id != (DeviceID{Major: 259, Minor: 12}) {
		t.Fatalf("parseDeviceID = %#v, %v", id, ok)
	}
	for _, value := range []string{"", "1", "a:1", "1:b", "1:2:3"} {
		if _, ok := parseDeviceID(value); ok {
			t.Fatalf("parseDeviceID(%q) succeeded", value)
		}
	}
}

func TestDiscoverPathUsesNearestExistingParent(t *testing.T) {
	dir := t.TempDir()
	want, ok := discoverPathLinux(dir)
	if !ok {
		t.Fatal("failed to discover temporary directory")
	}
	got, ok := discoverPathLinux(filepath.Join(dir, "missing", "torrent"))
	if !ok {
		t.Fatal("failed to discover missing path through its parent")
	}
	if got.id != want.id {
		t.Fatalf("device = %v, want %v", got.id, want.id)
	}
}
