// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package sys

import (
	"fmt"
	"regexp"
	"strconv"
	"sync"
	"syscall"

	"github.com/samber/lo"
)

var kernelMajor int
var kernelMinor int
var kernelOnce sync.Once
var kernelPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.\d.*`)

// KernelVersion return linux kernel version
// from https://go.dev/src/internal/syscall/unix/kernel_version_linux.go
func KernelVersion() (major, minor int) {
	kernelOnce.Do(func() {
		var uname syscall.Utsname
		if err := syscall.Uname(&uname); err != nil {
			panic(fmt.Sprintf("failed to get kernel version, please file a issue on https://github.com/trim21/neptune/issues\nerror: %v", err))
		}

		v := int8ToStr(uname.Release[:])
		m := kernelPattern.FindStringSubmatch(v)

		if len(m) != 3 {
			panic(fmt.Sprintf("unexpected kernel release %q, please file a issue on https://github.com/trim21/neptune/issues", v))
		}

		kernelMajor = lo.Must(strconv.Atoi(m[1]))
		kernelMinor = lo.Must(strconv.Atoi(m[2]))
	})

	return kernelMajor, kernelMinor
}

// A utility to convert the values to proper strings.
func int8ToStr(arr []int8) string {
	b := make([]byte, 0, len(arr))
	for _, v := range arr {
		if v == 0x00 {
			break
		}
		b = append(b, byte(v))
	}
	return string(b)
}
