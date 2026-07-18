// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package diskio

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const mountInfoPath = "/proc/self/mountinfo"

func init() {
	discoverPath = discoverPathLinux
	discoverDevices = discoverDevicesLinux
}

func discoverPathLinux(path string) (deviceInfo, bool) {
	var stat unix.Stat_t
	for {
		err := unix.Stat(path, &stat)
		if err == nil {
			id := DeviceID{Major: unix.Major(uint64(stat.Dev)), Minor: unix.Minor(uint64(stat.Dev))}
			return deviceInfo{id: id, class: classifyLinuxDevice(id)}, true
		}
		if !errors.Is(err, unix.ENOENT) {
			return deviceInfo{}, false
		}
		parent := filepath.Dir(path)
		if parent == path {
			return deviceInfo{}, false
		}
		path = parent
	}
}

func discoverDevicesLinux() []deviceInfo {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := make(map[DeviceID]struct{})
	var devices []deviceInfo
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		id, ok := parseDeviceID(fields[2])
		if !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		devices = append(devices, deviceInfo{id: id, class: classifyLinuxDevice(id)})
	}
	return devices
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
	min, err := strconv.ParseUint(minor, 10, 32)
	if err != nil {
		return DeviceID{}, false
	}
	return DeviceID{Major: uint32(maj), Minor: uint32(min)}, true
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
