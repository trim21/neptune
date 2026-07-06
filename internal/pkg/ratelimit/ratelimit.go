// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package ratelimit provides a token-bucket rate limiter for byte streams.
// A zero-value Limiter is a no-op (unlimited).
package ratelimit

import (
	"context"
	"sync"
	"time"

	"go.uber.org/atomic"
)

// maxSleep is the upper bound for a single Wait sleep cycle.
// It ensures that dynamic rate changes (via Update) and context cancellations
// are noticed within this interval, while adding negligible overhead for
// typical rates (MB/s range) where the computed sleep is far shorter.
const maxSleep = 250 * time.Millisecond

// Limiter controls the rate of byte transfers using a token bucket algorithm.
// Tokens represent bytes and are replenished at the configured rate.
// A zero-value Limiter imposes no limit.
type Limiter struct {
	// start is the reference time for token generation.
	// It is re-anchored on creation and every Update() call to ensure
	// rate changes are accurately reflected.
	start time.Time
	// consumed tracks how much time (in seconds) has already been
	// converted to tokens since start. This avoids the race where
	// concurrent refill calls overwrite a shared l.last, causing only
	// the first caller to receive accumulated tokens.
	consumed float64

	rate   atomic.Int64
	tokens float64
	burst  float64
	mu     sync.Mutex
}

func computeBurst(rate float64) float64 {
	// Burst of 2 seconds worth of tokens provides smooth traffic shaping
	// while absorbing TCP window fluctuations.
	// Minimum 512 KB ensures reasonable burst even at very low rates.
	const minBurst = 512 * 1024
	burst := rate * 2
	if burst < minBurst {
		burst = minBurst
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
		start:  time.Now(),
	}
}

// Update dynamically changes the rate limit.
// If rate <= 0, the limiter becomes unlimited.
// Waiting goroutines will notice the change within maxSleep.
func (l *Limiter) Update(rate int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if rate <= 0 {
		l.rate.Store(0)
		l.tokens = 0
		l.burst = 0
		l.consumed = 0
		return
	}

	r := float64(rate)
	burst := computeBurst(r)

	l.rate.Store(rate)
	l.burst = burst
	if l.tokens > burst {
		l.tokens = burst
	}
	// Re-anchor time origin to prevent rate changes from using stale
	// elapsed-time calculations with the wrong rate.
	l.start = time.Now()
	l.consumed = 0
}

// Wait blocks until n bytes worth of tokens are available or ctx is canceled.
// A zero-value Limiter returns immediately.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l.rate.Load() <= 0 {
		return nil
	}

	l.mu.Lock()
	l.refill()
	l.tokens -= float64(n)

	// Fast path: enough tokens available (including burst), return immediately.
	if l.tokens >= 0 {
		l.mu.Unlock()
		return nil
	}

	// Slow path: we are in token debt.
	// By going negative early (debt model), we prevent other goroutines from
	// consuming the same tokens, avoiding "token stealing" that causes jitter
	// when multiple peers share a limiter.
	// Each sleep cycle is capped at maxSleep so that dynamic rate changes via
	// Update() are noticed promptly.
	for {
		r := float64(l.rate.Load())
		if r <= 0 {
			// Rate became unlimited — clear debt and return.
			l.tokens = 0
			l.mu.Unlock()
			return nil
		}

		debt := -l.tokens
		waitTime := time.Duration(debt / r * float64(time.Second))
		waitTime = max(waitTime, time.Microsecond) // prevent busy-wait
		waitTime = min(waitTime, maxSleep)

		l.mu.Unlock()

		timer := time.NewTimer(waitTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Restore tokens that were reserved but never used.
			l.mu.Lock()
			l.tokens += float64(n)
			if l.tokens > l.burst {
				l.tokens = l.burst
			}
			l.mu.Unlock()
			return ctx.Err()
		case <-timer.C:
		}

		l.mu.Lock()
		l.refill()

		if l.rate.Load() <= 0 {
			l.tokens = 0
			l.mu.Unlock()
			return nil
		}

		// Debt repaid?
		if l.tokens >= 0 {
			l.mu.Unlock()
			return nil
		}
		// Still in debt, loop to wait more.
	}
}

// refill replenishes tokens based on elapsed time. Caller must hold l.mu.
// Uses a cumulative consumed-time counter instead of a mutable "last"
// timestamp. This ensures that every refill call adds only the time not
// yet accounted for by any previous refill, making token generation
// correct under concurrent calls. It also handles rate changes via
// Update() cleanly, since Update resets the start anchor.
func (l *Limiter) refill() {
	now := time.Now()
	totalElapsed := now.Sub(l.start).Seconds()
	unaccounted := totalElapsed - l.consumed
	if unaccounted <= 0 {
		return
	}
	l.consumed = totalElapsed

	l.tokens += unaccounted * float64(l.rate.Load())
	if l.tokens > l.burst {
		l.tokens = l.burst
	}
}

// Rate returns the configured rate in bytes per second. 0 means unlimited.
func (l *Limiter) Rate() int64 {
	return l.rate.Load()
}
