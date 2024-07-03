package global

import (
	"fmt"
	"net"
	"runtime"
	"time"
)

var Dialer = net.Dialer{
	Timeout: time.Minute,
}

var PeerIDPrefix = fmt.Sprintf("-TY%x%x%x0-", MAJOR, MINOR, PATCH)

const IsMacos = runtime.GOOS == "darwin"
const IsWindows = runtime.GOOS == "windows"
const IsLinux = runtime.GOOS == "linux"
