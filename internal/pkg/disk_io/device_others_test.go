// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package disk_io

import "testing"

func TestDiscoverPathReturnsDefaultDevice(t *testing.T) {
	device := discoverPath("ignored")
	if device.id != (DeviceID{}) || device.class != DeviceHDD {
		t.Fatalf("device = %#v", device)
	}
}
