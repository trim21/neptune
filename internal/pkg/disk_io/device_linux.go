// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package disk_io

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const mountInfoPath = "/proc/self/mountinfo"

func discoverPath(path string) deviceInfo {
	var stat unix.Stat_t
	for {
		err := unix.Stat(path, &stat)
		if err == nil {
			id := DeviceID{Major: unix.Major(stat.Dev), Minor: unix.Minor(stat.Dev)}
			return deviceInfo{id: id, class: classifyLinuxDevice(id)}
		}
		if !errors.Is(err, unix.ENOENT) {
			return defaultDeviceInfo()
		}
		parent := filepath.Dir(path)
		if parent == path {
			return defaultDeviceInfo()
		}
		path = parent
	}
}

func discoverDevices() []deviceInfo {
	data, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil
	}

	seen := make(map[DeviceID]struct{})
	var devices []deviceInfo
	for line := range strings.SplitSeq(string(data), "\n") {
		device, ok := parseMountInfoLine(line)
		if !ok {
			continue
		}
		if _, ok := seen[device.id]; ok {
			continue
		}
		seen[device.id] = struct{}{}
		device.class = classifyLinuxDevice(device.id)
		devices = append(devices, device)
	}
	return devices
}

func parseMountInfoLine(line string) (deviceInfo, bool) {
	fields := strings.Fields(line)
	separator := slices.Index(fields, "-")
	if len(fields) < 5 || separator < 0 || separator+1 >= len(fields) {
		return deviceInfo{}, false
	}
	id, ok := parseDeviceID(fields[2])
	if !ok {
		return deviceInfo{}, false
	}
	return deviceInfo{
		id:         id,
		filesystem: fields[separator+1],
		mountPoint: fields[4],
	}, true
}

func parseDeviceID(value string) (DeviceID, bool) {
	major, minor, ok := strings.Cut(value, ":")
	if !ok {
		return DeviceID{}, false
	}
	maj, err := strconv.ParseUint(major, 10, 32)
	if err != nil {
		return DeviceID{}, false
	}
	minVal, err := strconv.ParseUint(minor, 10, 32)
	if err != nil {
		return DeviceID{}, false
	}
	return DeviceID{Major: uint32(maj), Minor: uint32(minVal)}, true
}

func classifyLinuxDevice(id DeviceID) DeviceClass {
	path := filepath.Join("/sys/dev/block", id.String())
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return DeviceHDD
	}

	for current := realPath; current != filepath.Dir(current); current = filepath.Dir(current) {
		value, err := os.ReadFile(filepath.Join(current, "queue", "rotational"))
		if err == nil {
			if strings.TrimSpace(string(value)) == "0" {
				return DeviceSSD
			}
			return DeviceHDD
		}
	}
	return DeviceHDD
}
