// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tasks

import (
	"github.com/panjf2000/ants/v2"
	"github.com/samber/lo"
)

// verifyDispatchPool dispatches asynchronous piece verification callers. Actual disk
// concurrency is controlled by gfs.IOContext.
var verifyDispatchPool = lo.Must(ants.NewPool(64, ants.WithPreAlloc(true)))

// netPool handles network I/O tasks: connection handshake, HAVE broadcasts.
var netPool = lo.Must(ants.NewPool(200, ants.WithPreAlloc(true)))
