// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package tasks

func Submit(task func()) {
	_ = ioPool.Submit(task)
}

// SubmitNet submits a network I/O task: connection handshake, HAVE broadcasts, etc.
func SubmitNet(task func()) {
	_ = netPool.Submit(task)
}

// SubmitIO submits a file I/O task: piece verification, disk reads/writes.
func SubmitIO(task func()) {
	_ = ioPool.Submit(task)
}
