// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tracker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJitterIntervalRange(t *testing.T) {
	interval := 30 * time.Minute
	minDelta := 15 * time.Minute

	lo := 0.85 * float64(interval)
	hi := 1.15 * float64(interval)

	var hitLo, hitHi bool
	for range 2000 {
		got := jitterInterval(interval, minDelta)
		g := float64(got)
		assert.GreaterOrEqual(t, g, lo, "must not jitter below -15%%")
		assert.LessOrEqual(t, g, hi, "must not jitter above +15%%")
		if g < lo+float64(time.Minute) {
			hitLo = true
		}
		if g > hi-float64(time.Minute) {
			hitHi = true
		}
	}

	assert.True(t, hitLo, "jitter should reach the lower bound")
	assert.True(t, hitHi, "jitter should reach the upper bound")
}

func TestJitterIntervalClampsToMinDelta(t *testing.T) {
	// When minDelta sits above the lower jitter bound, the result must be
	// clamped so the tracker's min_interval is never violated.
	interval := 10 * time.Minute
	minDelta := 9 * time.Minute // > 0.85*interval = 8.5min

	for range 1000 {
		got := jitterInterval(interval, minDelta)
		assert.GreaterOrEqual(t, got, minDelta, "must never announce before min_interval")
		assert.LessOrEqual(t, got, time.Duration(1.15*float64(interval)), "must not jitter above +15%%")
	}
}
