// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDialRateLimiterUnlimited(t *testing.T) {
	l := NewDialRateLimiter(0)
	for range 1000 {
		assert.True(t, l.TryAcquire(), "rate<=0 must never block")
	}
}

func TestDialRateLimiterCapsBurst(t *testing.T) {
	l := NewDialRateLimiter(5) // burst == rate == 5
	for range 5 {
		assert.True(t, l.TryAcquire())
	}
	assert.False(t, l.TryAcquire(), "burst budget must be exhausted")
}

func TestDialRateLimiterRefills(t *testing.T) {
	l := NewDialRateLimiter(5) // 5/s → 1 token per 200ms
	for range 5 {
		assert.True(t, l.TryAcquire())
	}
	assert.False(t, l.TryAcquire())

	time.Sleep(250 * time.Millisecond)
	assert.True(t, l.TryAcquire(), "token should be replenished after 250ms")
	assert.False(t, l.TryAcquire(), "only one token should have been replenished")
}

func TestDialRateLimiterSustainedRate(t *testing.T) {
	l := NewDialRateLimiter(10)

	// Warm up: consume the initial burst.
	for range 10 {
		assert.True(t, l.TryAcquire())
	}

	// Over the next second the limiter must allow at most ~10 more attempts.
	deadline := time.Now().Add(1050 * time.Millisecond)
	allowed := 0
	for time.Now().Before(deadline) && allowed < 20 {
		if l.TryAcquire() {
			allowed++
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	assert.GreaterOrEqual(t, allowed, 8, "should approach the configured rate over 1s")
	assert.LessOrEqual(t, allowed, 12, "must not exceed the configured rate over 1s")
}
