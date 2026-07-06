// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package ratelimit

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFairnessDetailed investigates the token distribution fairness among
// multiple goroutines sharing a single rate limiter.
func TestFairnessDetailed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running fairness test in short mode")
	}

	rates := []int64{100_000, 1_000_000, 5_000_000}
	goroutineCounts := []int{2, 4, 8}

	for _, rate := range rates {
		for _, numG := range goroutineCounts {
			t.Run(fmt.Sprintf("rate_%dKB_%dgoroutines", rate/1000, numG), func(t *testing.T) {
				testFairness(t, rate, numG)
			})
		}
	}
}

func testFairness(t *testing.T, rate int64, numGoroutines int) {
	l := New(rate)

	// Deplete burst.
	if err := depleteBurst(l, rate); err != nil {
		t.Fatal(err)
	}

	var perGoroutineBytes []atomic.Int64
	perGoroutineBytes = make([]atomic.Int64, numGoroutines)

	// Use a longer duration to get stable results.
	duration := 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Each goroutine calls Wait with different block sizes
			// to simulate realistic behavior.
			for ctx.Err() == nil {
				// Vary block size per goroutine to simulate real BT traffic.
				blockSize := 16384 // default 16KB
				if idx%2 == 0 {
					blockSize = 8192 // 8KB
				}

				if err := l.Wait(ctx, blockSize); err != nil {
					return
				}
				perGoroutineBytes[idx].Add(int64(blockSize))

				// Yield to encourage fair scheduling.
				runtime.Gosched()
			}
		}(i)
	}

	wg.Wait()

	// Compute statistics.
	values := make([]float64, numGoroutines)
	var sum float64
	for i := range numGoroutines {
		v := float64(perGoroutineBytes[i].Load())
		values[i] = v
		sum += v
	}
	mean := sum / float64(numGoroutines)

	var variance float64
	minVal := values[0]
	maxVal := values[0]
	for _, v := range values {
		d := v - mean
		variance += d * d
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	variance /= float64(numGoroutines)
	stddev := math.Sqrt(variance)
	cv := stddev / mean // coefficient of variation

	totalRate := sum / duration.Seconds()

	t.Logf("per-goroutine bytes: %v", values)
	t.Logf("total=%.2f MB, mean=%d KB, CV=%.4f, min/max=%.2f",
		sum/1e6, int64(mean)/1024, cv, minVal/max(minVal, 1))
	t.Logf("actual_total_rate=%.2f KB/s, target=%d KB/s",
		totalRate/1024, rate/1024)

	// Total throughput should still be accurate.
	ratio := totalRate / float64(rate)
	if ratio < 0.85 || ratio > 1.15 {
		t.Errorf("total throughput off: ratio=%.3f", ratio)
	}

	// CV > 0.4 indicates significant unfairness.
	if cv > 0.4 {
		t.Logf("WARNING: high unfairness: CV=%.4f. "+
			"This indicates the limiter may not distribute tokens fairly "+
			"among concurrent goroutines, especially at low rates.", cv)
	}

	// A goroutine getting less than 10% of fair share is a red flag.
	if mean > 0 && minVal < mean*0.1 {
		t.Errorf("SEVERE starvation: one goroutine got only %.0f bytes (%.1f%% of mean)",
			minVal, minVal/max(mean, 1)*100)
	}
}

// TestRefillOverwrite demonstrates the issue where refill updates l.last,
// causing subsequent refills by other goroutines to miss time.
func TestRefillOverwrite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping refill-overwrite demonstration in short mode")
	}

	l := New(1000) // 1 KB/s

	// Deplete burst.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	// Simulate two goroutines: G1 is in slow path (unlocked),
	// then G2 calls refill and updates l.last, "stealing" the time
	// that G1 would have counted when it wakes up.

	// G1: reserves 1KB worth of tokens
	l.mu.Lock()
	l.refill()
	beforeTokens := l.tokens
	l.tokens -= 1024
	afterTokens := l.tokens
	l.mu.Unlock()

	t.Logf("G1: tokens before=%f, after=%f", beforeTokens, afterTokens)

	// Simulate waiting...
	time.Sleep(100 * time.Millisecond)

	// G2: calls Wait which triggers refill
	start2 := time.Now()
	if err := l.Wait(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	t.Logf("G2: Wait(500) returned in %v", time.Since(start2))

	// Now G1 continues its slow path...
	l.mu.Lock()
	l.refill()
	afterRefill := l.tokens
	l.mu.Unlock()

	t.Logf("G1: after refill tokens=%f", afterRefill)

	// After 100ms at 1KB/s, we should have accumulated ~100 bytes.
	// If G2's refill consumed those tokens, G1 would still be negative.
	// But the debt model means tokens ARE shared correctly — G1's debt
	// and G2's consumption both reduce the same token pool.

	// The key question: did the 100ms of elapsed time produce correct
	// number of tokens? G1 refill at start, 100ms passes, G2 refill at T+100ms.
	// G2's refill sees 100ms elapsed → 100 tokens. G2 uses 500 tokens.
	// G1's refill (right after G2) sees 0ms elapsed → 0 tokens.
	// Total tokens generated: 100. But we need 1024+500=1524 tokens total.
	// This is correct — the tokens from the 100ms are counted once.

	// The issue is that if G1 and G2 keep interleaving like this,
	// G1 might not accumulate tokens fast enough.
}

// BenchmarkLimiterOverhead measures the per-call overhead of the limiter
// when the rate is unlimited (fast path).
func BenchmarkLimiterOverhead(b *testing.B) {
	// Use rate=0 (unlimited) to measure the pure overhead of the fast path.
	var l Limiter
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_ = l.Wait(ctx, 16*1024)
	}
}

// BenchmarkLimiterOverheadNonZero measures overhead when rate is set but
// there are enough tokens (fast path with lock).
func BenchmarkLimiterOverheadNonZero(b *testing.B) {
	l := New(100_000_000) // 100 MB/s, burst = 200 MB pre-filled
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		_ = l.Wait(ctx, 16*1024) // stays in burst, fast path
	}
}

// BenchmarkLimiterSlowPath measures the slow path overhead per iteration.
func BenchmarkLimiterSlowPath(b *testing.B) {
	l := New(10_000_000) // 10 MB/s
	// Deplete burst to force slow path.
	_ = l.Wait(context.Background(), 512*1024)

	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		// 16KB at 10 MB/s waitTime = 16*1024/10e6 = 1.6ms (under 250ms cap)
		_ = l.Wait(ctx, 16*1024)
	}
}

// BenchmarkLimiterConcurrentOverhead measures contention overhead with
// unlimited rate (fast path only, lock contention).
func BenchmarkLimiterConcurrentOverhead(b *testing.B) {
	l := New(100_000_000) // large rate, fast path
	ctx := context.Background()

	b.Run("4_goroutines", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = l.Wait(ctx, 16*1024)
			}
		})
	})

	b.Run("16_goroutines", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = l.Wait(ctx, 16*1024)
			}
		})
	})
}

// BenchmarkLimiterSlowPathConcurrent measures slow path throughput with
// multiple concurrent goroutines.
func BenchmarkLimiterSlowPathConcurrent(b *testing.B) {
	rate := int64(100_000_000) // 100 MB/s
	l := New(rate)
	_ = l.Wait(context.Background(), 512*1024)

	ctx := context.Background()

	b.Run("4_goroutines", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = l.Wait(ctx, 16*1024)
			}
		})
	})

	b.Run("16_goroutines", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = l.Wait(ctx, 16*1024)
			}
		})
	})
}
