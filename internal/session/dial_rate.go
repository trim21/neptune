// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package session

import (
	"sync"
	"time"
)

// DialRateLimiter caps the rate of outgoing connection attempts (dials) at
// rate per second, using a mutex-guarded token bucket. Connections are heavy
// resources, so a simple mutex is preferred over lock-free bookkeeping.
//
// The burst is one second of budget, mirroring libtorrent's connection_speed
// quota: at most rate attempts per second, allowed to be spent in a short
// burst. A rate <= 0 means unlimited (no-op).
type DialRateLimiter struct {
	last   time.Time
	rate   float64
	burst  float64
	tokens float64
	mu     sync.Mutex
}

// NewDialRateLimiter creates a limiter allowing rate attempts per second.
// rate <= 0 returns an unlimited (no-op) limiter.
func NewDialRateLimiter(rate int64) *DialRateLimiter {
	if rate <= 0 {
		return &DialRateLimiter{}
	}

	r := float64(rate)
	return &DialRateLimiter{
		rate:   r,
		burst:  r,
		tokens: r,
		last:   time.Now(),
	}
}

// TryAcquire consumes one token if available. Returns false when the
// per-second budget is exhausted; the caller should retry later (the
// connect loop ticks once per second) instead of spinning.
func (l *DialRateLimiter) TryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.rate <= 0 {
		return true // unlimited
	}

	now := time.Now()
	l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
	l.last = now

	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}
