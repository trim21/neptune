// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package tasks

func Submit(task func()) {
	_ = verifyDispatchPool.Submit(task)
}

// SubmitNet submits a network I/O task: connection handshake, HAVE broadcasts, etc.
func SubmitNet(task func()) {
	_ = netPool.Submit(task)
}

// SubmitIO dispatches a piece verification caller. Disk concurrency is
// controlled by gfs.IOContext inside the FileStore operation.
func SubmitIO(task func()) {
	_ = verifyDispatchPool.Submit(task)
}
