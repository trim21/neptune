// SPDX-License-Identifier: MIT
// Copyright (c) 2023 Sourcegraph
//
// Adapted from github.com/sourcegraph/conc/panics (MIT license).

package conc

// Try executes f, catching and returning any panic it might spawn.
//
// The recovered panic can be propagated with panic(), or handled as a normal error with
// (*Recovered).AsError().
func Try(f func()) *Recovered {
	var c Catcher
	c.Try(f)
	return c.Recovered()
}
