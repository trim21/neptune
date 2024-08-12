// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package gsync

import (
	"sync"

	"neptune/internal/pkg/empty"
)

// Cond is a chan based sync.Cond can be selected.
// this help background goroutine to avoid wait on a chan.
type Cond struct {
	L sync.Locker
	C chan empty.Empty
}

func NewCond(l sync.Locker) *Cond {
	return &Cond{C: make(chan empty.Empty), L: l}
}

func (t *Cond) Wait() {
	t.L.Unlock()
	<-t.C
	t.L.Lock()
}

func (t *Cond) Signal() {
	t.signal()
}

func (t *Cond) Broadcast() {
	for {
		// Stop when we run out of waiters
		if !t.signal() {
			return
		}
	}
}

func (t *Cond) signal() bool {
	select {
	case t.C <- empty.Empty{}:
		return true
	default:
		return false
	}
}
