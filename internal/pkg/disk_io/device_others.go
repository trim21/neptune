// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !linux

package disk_io

func discoverPath(string) deviceInfo {
	return defaultDeviceInfo()
}

func discoverDevices() []deviceInfo {
	return []deviceInfo{defaultDeviceInfo()}
}
