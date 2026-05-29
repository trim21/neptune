// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package ratelimit provides a simple token-bucket rate limiter for byte streams.
// A zero Limiter is a no-op (unlimited).
package ratelimit

import (
	"context"
	"sync"
	"time"

	"go.uber.org/atomic"
)

// Limiter controls the rate of byte transfers using a token bucket algorithm.
// Tokens represent bytes and are replenished at the configured rate.
// A zero value Limiter imposes no limit.
type Limiter struct {
	last   time.Time
	rate   atomic.Int64
	tokens float64
	burst  float64
	mu     sync.Mutex
}

func computeBurst(rate float64) float64 {
	burst := rate
	if burst < 256*1024 {
		burst = 256 * 1024
	}
	return burst
}

// New creates a Limiter that allows rate bytes per second.
// If rate <= 0, the returned Limiter imposes no limit.
func New(rate int64) *Limiter {
	if rate <= 0 {
		return &Limiter{}
	}

	r := float64(rate)
	burst := computeBurst(r)

	return &Limiter{
		rate:   *atomic.NewInt64(rate),
		tokens: burst,
		burst:  burst,
		last:   time.Now(),
	}
}

// Update dynamically changes the rate limit.
// If rate <= 0, the limiter becomes unlimited.
func (l *Limiter) Update(rate int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if rate <= 0 {
		l.rate.Store(0)
		l.tokens = 0
		l.burst = 0
		return
	}

	r := float64(rate)
	burst := computeBurst(r)

	l.rate.Store(rate)
	l.burst = burst
	if l.tokens > burst {
		l.tokens = burst
	}
}

// Wait blocks until n bytes worth of tokens are available or ctx is canceled.
// A zero-value Limiter returns immediately.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l.rate.Load() <= 0 {
		return nil
	}

	l.mu.Lock()
	l.refill()

	if l.tokens >= float64(n) {
		l.tokens -= float64(n)
		l.mu.Unlock()
		return nil
	}

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		r := float64(l.rate.Load())
		if r <= 0 {
			l.mu.Unlock()
			return nil
		}

		deficit := float64(n) - l.tokens
		wait := max(time.Duration(deficit/r*float64(time.Second)), time.Millisecond)

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		l.mu.Unlock()

		select {
		case <-ctx.Done():
			l.mu.Lock()
			return ctx.Err()
		case <-timer.C:
		}

		l.mu.Lock()
		l.refill()

		if l.rate.Load() <= 0 {
			return nil
		}

		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
	}
}

// refill replenishes tokens based on elapsed time. Caller must hold l.mu.
func (l *Limiter) refill() {
	now := time.Now()
	elapsed := now.Sub(l.last).Seconds()
	l.last = now

	l.tokens += elapsed * float64(l.rate.Load())
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
}

// Rate returns the configured rate in bytes per second. 0 means unlimited.
func (l *Limiter) Rate() int64 {
	return l.rate.Load()
}
