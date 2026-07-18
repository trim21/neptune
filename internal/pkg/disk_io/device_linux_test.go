// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package disk_io

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

func TestParseMountInfoLine(t *testing.T) {
	device, ok := parseMountInfoLine("36 25 8:1 / /mnt/data rw,relatime - ext4 /dev/sda1 rw")
	if !ok {
		t.Fatal("parseMountInfoLine failed")
	}
	if device.id != (DeviceID{Major: 8, Minor: 1}) || device.filesystem != "ext4" || device.mountPoint != "/mnt/data" {
		t.Fatalf("device = %#v", device)
	}
}

func TestDiscoverPathUsesNearestExistingParent(t *testing.T) {
	dir := t.TempDir()
	want := discoverPath(dir)
	got := discoverPath(filepath.Join(dir, "missing", "torrent"))
	if got.id != want.id {
		t.Fatalf("device = %v, want %v", got.id, want.id)
	}
}
