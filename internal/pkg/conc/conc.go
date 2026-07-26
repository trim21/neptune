// SPDX-License-Identifier: MIT
// Copyright (c) 2023 Sourcegraph
//
// Adapted from github.com/sourcegraph/conc (MIT license).

package conc

import "sync"

// NewWaitGroup creates a new WaitGroup.
func NewWaitGroup() *WaitGroup {
	return &WaitGroup{}
}

// WaitGroup is the primary building block for scoped concurrency.
// Goroutines can be spawned in the WaitGroup with the Go method,
// and calling Wait() will ensure that each of those goroutines exits
// before continuing. Any panics in a child goroutine will be caught
// and propagated to the caller of Wait().
//
// The zero value of WaitGroup is usable, just like sync.WaitGroup.
// Also like sync.WaitGroup, it must not be copied after first use.
type WaitGroup struct {
	pc Catcher
	wg sync.WaitGroup
}

// Go spawns a new goroutine in the WaitGroup.
func (h *WaitGroup) Go(f func()) {
	h.wg.Go(func() {
		h.pc.Try(f)
	})
}

// Wait will block until all goroutines spawned with Go exit and will
// propagate any panics spawned in a child goroutine.
func (h *WaitGroup) Wait() {
	h.wg.Wait()

	// Propagate a panic if we caught one from a child goroutine.
	h.pc.Repanic()
}

// WaitAndRecover will block until all goroutines spawned with Go exit and
// will return a *Recovered if one of the child goroutines panics.
func (h *WaitGroup) WaitAndRecover() *Recovered {
	h.wg.Wait()

	// Return a recovered panic if we caught one from a child goroutine.
	return h.pc.Recovered()
}
