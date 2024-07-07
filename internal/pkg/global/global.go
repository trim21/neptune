// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package global

import (
	"net"
	"runtime"
	"time"
)

var Dialer = net.Dialer{
	Timeout: time.Minute,
}

const IsMacos = runtime.GOOS == "darwin"
const IsWindows = runtime.GOOS == "windows"
const IsLinux = runtime.GOOS == "linux"
