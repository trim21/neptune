// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/atomic"
)

// TestThroughputSteadyState simulates multiple concurrent goroutines
// (like multiple torrents downloading through a shared global limiter)
// and verifies the actual throughput matches the configured rate.
func TestThroughputSteadyState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running throughput test in short mode")
	}

	tests := []struct {
		name       string
		rate       int64 // bytes per second
		goroutines int   // concurrent workers
		blockSize  int   // bytes per Wait call
		duration   time.Duration
		tolerance  float64 // allowed deviation from target rate
	}{
		{"1MB_2goroutines_16KB", 1_000_000, 2, 16 * 1024, 5 * time.Second, 0.15},
		{"1MB_4goroutines_16KB", 1_000_000, 4, 16 * 1024, 5 * time.Second, 0.15},
		{"1MB_8goroutines_16KB", 1_000_000, 8, 16 * 1024, 5 * time.Second, 0.15},
		{"5MB_4goroutines_16KB", 5_000_000, 4, 16 * 1024, 5 * time.Second, 0.15},
		{"5MB_8goroutines_16KB", 5_000_000, 8, 16 * 1024, 5 * time.Second, 0.15},
		{"5MB_16goroutines_16KB", 5_000_000, 16, 16 * 1024, 5 * time.Second, 0.15},
		{"10MB_16goroutines_16KB", 10_000_000, 16, 16 * 1024, 3 * time.Second, 0.15},
		// Variable block sizes to simulate real BT traffic.
		{"1MB_4goroutines_variableBlocks", 1_000_000, 4, 0, 5 * time.Second, 0.15},
		// Low rate test
		{"100KB_4goroutines_16KB", 100_000, 4, 16 * 1024, 5 * time.Second, 0.20},
		// Edge case: very high rate
		{"50MB_16goroutines_16KB", 50_000_000, 16, 16 * 1024, 3 * time.Second, 0.20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(tt.rate)

			// Deplete the full burst before measuring steady-state.
			// burst = max(rate*2, 512KB), so we consume the larger of the two.
			burstToConsume := 512 * 1024
			if rateBurst := int(tt.rate * 2); rateBurst > burstToConsume {
				burstToConsume = rateBurst
			}
			if err := l.Wait(context.Background(), burstToConsume); err != nil {
				t.Fatal(err)
			}

			var totalBytes atomic.Int64
			ctx, cancel := context.WithTimeout(context.Background(), tt.duration+time.Second)
			defer cancel()

			var wg sync.WaitGroup
			start := time.Now()

			for range tt.goroutines {
				wg.Go(func() {
					for ctx.Err() == nil {
						blockSize := tt.blockSize
						if blockSize == 0 {
							// Variable block sizes between 8KB and 64KB.
							blockSize = 8*1024 + int(time.Now().UnixNano()%int64(56*1024))
						}
						if err := l.Wait(ctx, blockSize); err != nil {
							return
						}
						totalBytes.Add(int64(blockSize))
					}
				})
			}

			// Wait for the measurement duration.
			time.Sleep(tt.duration)
			cancel()
			wg.Wait()

			elapsed := time.Since(start)
			// Use the shorter of elapsed and duration for rate calculation
			// (elapsed might be slightly longer than duration due to goroutine cleanup).
			effectiveDuration := elapsed
			if effectiveDuration > tt.duration+500*time.Millisecond {
				effectiveDuration = tt.duration
			}

			actualRate := float64(totalBytes.Load()) / effectiveDuration.Seconds()
			targetRate := float64(tt.rate)
			ratio := actualRate / targetRate

			t.Logf("target=%.2f MB/s, actual=%.2f MB/s, ratio=%.3f, bytes=%d, elapsed=%v",
				targetRate/1e6, actualRate/1e6, ratio, totalBytes.Load(), elapsed)

			if ratio < 1.0-tt.tolerance {
				t.Errorf("throughput too low: %.2f%% of target (tolerance %.0f%%)",
					ratio*100, (1.0-tt.tolerance)*100)
			}
			if ratio > 1.0+tt.tolerance {
				t.Errorf("throughput too high: %.2f%% of target (tolerance %.0f%%)",
					ratio*100, (1.0+tt.tolerance)*100)
			}
		})
	}
}

func depleteBurst(l *Limiter, rate int64) error {
	burstToConsume := 512 * 1024
	if rateBurst := int(rate * 2); rateBurst > burstToConsume {
		burstToConsume = rateBurst
	}
	return l.Wait(context.Background(), burstToConsume)
}

// TestBurstNotPermanent verifies that after burst is depleted, the rate
// returns to the configured steady-state limit.
func TestBurstNotPermanent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running test in short mode")
	}

	rate := int64(1_000_000) // 1 MB/s
	l := New(rate)

	// Deplete the full burst.
	if err := depleteBurst(l, rate); err != nil {
		t.Fatal(err)
	}

	// Now measure throughput over 2 seconds — should be close to 1 MB/s.
	start := time.Now()
	var total int64

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for ctx.Err() == nil {
		if err := l.Wait(ctx, 16*1024); err != nil {
			break
		}
		total += 16 * 1024
	}

	elapsed := time.Since(start).Seconds()
	actualRate := float64(total) / elapsed

	// After burst is depleted, rate should be ~1 MB/s within 15%.
	targetRate := float64(rate)
	if actualRate < targetRate*0.85 || actualRate > targetRate*1.15 {
		t.Errorf("post-burst rate = %.2f MB/s, expected ~%.2f MB/s (elapsed=%.2fs, bytes=%d)",
			actualRate/1e6, targetRate/1e6, elapsed, total)
	}
	t.Logf("post-burst rate = %.2f MB/s", actualRate/1e6)
}

// TestConcurrentUpdateDrain verifies that concurrent Update() calls while
// goroutines are waiting in the slow path do not cause token loss.
func TestConcurrentUpdateDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running test in short mode")
	}

	rate := int64(2_000_000) // 2 MB/s
	l := New(rate)

	// Deplete burst.
	if err := depleteBurst(l, rate); err != nil {
		t.Fatal(err)
	}

	var totalBytes atomic.Int64
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Start 4 download goroutines.
	for range 4 {
		wg.Go(func() {
			for ctx.Err() == nil {
				if err := l.Wait(ctx, 16*1024); err != nil {
					return
				}
				totalBytes.Add(16 * 1024)
			}
		})
	}

	// Periodically change the rate to verify no token loss.
	rates := []int64{2_000_000, 1_000_000, 3_000_000, 2_000_000, 1_500_000, 2_000_000}
	rateIdx := 0
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	start := time.Now()
	done := time.After(3 * time.Second)

loop:
	for {
		select {
		case <-done:
			break loop
		case <-ticker.C:
			l.Update(rates[rateIdx%len(rates)])
			rateIdx++
		}
	}

	cancel()
	wg.Wait()

	elapsed := time.Since(start).Seconds()

	// Compute expected bytes: average rate over the period.
	// Rates cycle every 3s (6 steps × 0.5s).
	// Average of [2, 1, 3, 2, 1.5, 2] = 11.5/6 ≈ 1.917 MB/s
	// Over 3s: ~5.75 MB. Allow wide tolerance due to rate changes.
	expectedBytes := float64(3.0) * 1.917e6
	actualBytes := float64(totalBytes.Load())
	ratio := actualBytes / expectedBytes

	t.Logf("actual=%.2f MB, expected~%.2f MB, elapsed=%.2fs, ratio=%.3f",
		actualBytes/1e6, expectedBytes/1e6, elapsed, ratio)

	if ratio < 0.7 || ratio > 1.3 {
		t.Errorf("unexpected byte count with concurrent updates: ratio=%.3f", ratio)
	}
}

// TestNoTokenLeak verifies that tokens are not lost due to concurrency.
// We run a fixed workload (total N bytes) through M goroutines and verify
// it completes in the expected time.
func TestNoTokenLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running leak test in short mode")
	}

	tests := []struct {
		name       string
		rate       int64
		goroutines int
		totalBytes int64
		blockSize  int
	}{
		{"small", 1_000_000, 4, 5_000_000, 16 * 1024},
		{"medium", 5_000_000, 8, 20_000_000, 16 * 1024},
		{"large_blocks", 1_000_000, 4, 5_000_000, 128 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := New(tt.rate)

			// Deplete burst.
			if err := depleteBurst(l, tt.rate); err != nil {
				t.Fatal(err)
			}

			var remaining atomic.Int64
			remaining.Store(tt.totalBytes)
			ctx := context.Background()

			start := time.Now()
			var wg sync.WaitGroup

			for range tt.goroutines {
				wg.Go(func() {
					for {
						n := remaining.Add(-int64(tt.blockSize))
						if n < 0 {
							// Put back what we took.
							remaining.Add(int64(tt.blockSize))
							return
						}
						_ = l.Wait(ctx, tt.blockSize)
					}
				})
			}

			wg.Wait()
			elapsed := time.Since(start).Seconds()

			expectedTime := float64(tt.totalBytes) / float64(tt.rate)

			ratio := elapsed / expectedTime
			t.Logf("elapsed=%.3fs, expected=%.3fs, ratio=%.3f", elapsed, expectedTime, ratio)

			if ratio < 0.85 {
				t.Errorf("completed too fast: %.2f%% of expected time (tokens lost?)", ratio*100)
			}
			if ratio > 1.25 {
				t.Errorf("completed too slow: %.2f%% of expected time (possible token leak)", ratio*100)
			}
		})
	}
}

// TestSmallBlocksAccuracy tests the limiter with very small block sizes,
// which can expose precision issues.
func TestSmallBlocksAccuracy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping small-block accuracy test in short mode")
	}

	rate := int64(100_000) // 100 KB/s
	l := New(rate)

	// Deplete burst.
	if err := depleteBurst(l, rate); err != nil {
		t.Fatal(err)
	}

	// Try to transfer 200 KB with 1 KB blocks.
	// Expected: 200 KB / 100 KB/s = 2 seconds.
	totalBytes := 200 * 1024
	blockSize := 1024

	start := time.Now()
	ctx := context.Background()

	for range totalBytes / blockSize {
		if err := l.Wait(ctx, blockSize); err != nil {
			t.Fatal(err)
		}
	}

	elapsed := time.Since(start).Seconds()
	expectedTime := float64(totalBytes) / float64(rate)

	t.Logf("elapsed=%.3fs, expected=%.3fs", elapsed, expectedTime)

	if elapsed < expectedTime*0.85 {
		t.Errorf("too fast: %.3fs < %.3fs (%.0f%%)", elapsed, expectedTime*0.85, elapsed/expectedTime*100)
	}
	if elapsed > expectedTime*1.20 {
		t.Errorf("too slow: %.3fs > %.3fs (%.0f%%)", elapsed, expectedTime*1.20, elapsed/expectedTime*100)
	}
}

// TestUpdateTokensPreserved verifies that Update() does not lose tokens
// that were accumulated but not yet consumed.
func TestUpdateTokensPreserved(t *testing.T) {
	// Create a limiter and let it accumulate tokens.
	l := New(10_000) // 10 KB/s

	// Wait 500ms to accumulate some tokens (but don't consume them).
	time.Sleep(500 * time.Millisecond)

	// Now call Update with a new rate — the accumulated tokens
	// should still allow immediate consumption up to the new burst.
	l.Update(10_000)

	// Try to consume 5 KB (0.5s worth at 10 KB/s).
	// This should complete very quickly since tokens were accumulated.
	start := time.Now()
	if err := l.Wait(context.Background(), 5*1024); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Should be nearly instant (much less than 500ms).
	if elapsed > 50*time.Millisecond {
		t.Errorf("Update() appears to have lost accumulated tokens: waited %v for 5KB", elapsed)
	}
}

// TestRateChangeInSlowPath specifically tests the scenario where the rate
// is changed while a goroutine is sleeping in the slow path.
func TestRateChangeInSlowPath(t *testing.T) {
	l := New(1000) // 1 KB/s

	// Deplete burst.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	// Start a goroutine that needs 2 KB at 1 KB/s → should take ~2s.
	// But we'll increase the rate to 10 KB/s after 200ms.
	done := make(chan time.Duration, 1)
	start := time.Now()

	go func() {
		if err := l.Wait(context.Background(), 2048); err != nil {
			t.Error(err)
		}
		done <- time.Since(start)
	}()

	time.Sleep(200 * time.Millisecond)
	l.Update(10000) // Increase to 10 KB/s.

	elapsed := <-done
	t.Logf("completed in %v", elapsed)

	// After rate increase to 10 KB/s, the remaining 1.8 KB should take ~0.18s.
	// Total: ~0.2s + 0.18s = ~0.38s.
	// But the slow path checks rate on each sleep cycle (max 250ms),
	// so worst case: 200ms + 250ms + 180ms ≈ 630ms.
	expectedMax := 800 * time.Millisecond
	if elapsed > expectedMax {
		t.Errorf("rate change not picked up promptly: %v > %v", elapsed, expectedMax)
	}
}

// TestLargeNumberOfConcurrentGoroutines stress-tests the limiter with
// many concurrent callers.
func TestLargeNumberOfConcurrentGoroutines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	rate := int64(10_000_000) // 10 MB/s
	l := New(rate)

	// Deplete burst.
	if err := depleteBurst(l, rate); err != nil {
		t.Fatal(err)
	}

	const numGoroutines = 50
	var totalBytes atomic.Int64

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	start := time.Now()

	for range numGoroutines {
		wg.Go(func() {
			for ctx.Err() == nil {
				// Vary block sizes.
				blockSize := 4*1024 + int(time.Now().UnixNano()%int64(28*1024))
				if err := l.Wait(ctx, blockSize); err != nil {
					return
				}
				totalBytes.Add(int64(blockSize))
			}
		})
	}

	wg.Wait()
	elapsed := time.Since(start).Seconds()

	actualRate := float64(totalBytes.Load()) / elapsed
	ratio := actualRate / float64(rate)

	t.Logf("50 goroutines: actual=%.2f MB/s, target=%.2f MB/s, ratio=%.3f",
		actualRate/1e6, float64(rate)/1e6, ratio)

	if ratio < 0.80 || ratio > 1.20 {
		t.Errorf("unexpected throughput with 50 goroutines: ratio=%.3f", ratio)
	}
}
