// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestZeroLimiterIsUnlimited(t *testing.T) {
	var l Limiter
	start := time.Now()
	err := l.Wait(context.Background(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("zero limiter should not block")
	}
}

func TestNewZeroRateIsUnlimited(t *testing.T) {
	l := New(0)
	start := time.Now()
	err := l.Wait(context.Background(), 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("zero-rate limiter should not block")
	}
}

func TestRate(t *testing.T) {
	if r := New(1000).Rate(); r != 1000 {
		t.Fatalf("expected rate 1000, got %d", r)
	}
	if r := New(0).Rate(); r != 0 {
		t.Fatalf("expected rate 0, got %d", r)
	}
	if r := new(Limiter).Rate(); r != 0 {
		t.Fatalf("expected rate 0, got %d", r)
	}
}

func TestUpdate(t *testing.T) {
	l := New(1000)
	if r := l.Rate(); r != 1000 {
		t.Fatalf("expected rate 1000, got %d", r)
	}

	l.Update(2000)
	if r := l.Rate(); r != 2000 {
		t.Fatalf("expected rate 2000, got %d", r)
	}

	l.Update(0)
	if r := l.Rate(); r != 0 {
		t.Fatalf("expected rate 0, got %d", r)
	}
}

func TestLimiterBlocks(t *testing.T) {
	l := New(100)

	// Exhaust the 512 KB min burst first.
	start := time.Now()
	err := l.Wait(context.Background(), 512*1024)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("first call should not block significantly")
	}

	// Now tokens are empty. 100 bytes at 100 B/s → ~1s.
	start = time.Now()
	err = l.Wait(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 500*time.Millisecond {
		t.Fatalf("expected ~1s block, got %v", elapsed)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("expected ~1s block, got %v (too slow)", elapsed)
	}
}

func TestLimiterContextCancel(t *testing.T) {
	l := New(1)

	// Exhaust burst so the next Wait enters the slow path.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := l.Wait(ctx, 1)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestUpdateToUnlimitedWhileWaiting(t *testing.T) {
	l := New(1)

	// Exhaust burst so the next Wait enters the slow path.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		// 256 KB at 1 B/s — would block for days without the Update.
		done <- l.Wait(context.Background(), 256*1024)
	}()

	time.Sleep(50 * time.Millisecond)
	l.Update(0)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after Update(0)")
	}
}

func TestConcurrentWaitFairness(t *testing.T) {
	// Two goroutines sharing one limiter: wall-clock time should be
	// ~ total_bytes / rate, not dominated by token stealing.
	l := New(1000) // 1 KB/s

	// Exhaust burst first.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	start := time.Now()

	for range 2 {
		go func() {
			defer wg.Done()
			// Each needs 500 bytes at 1 KB/s → wall clock ~1s total.
			if err := l.Wait(context.Background(), 500); err != nil {
				t.Error(err)
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)

	// 1000 bytes at 1000 B/s = 1s wall clock.
	if elapsed < 700*time.Millisecond {
		t.Fatalf("concurrent wait too fast: %v (possible token stealing)", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("concurrent wait too slow: %v", elapsed)
	}
}

func TestUpdateToLowerRateWhileWaiting(t *testing.T) {
	l := New(2000) // 2 KB/s

	// Exhaust burst.
	if err := l.Wait(context.Background(), 512*1024); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		// 2 KB at 2 KB/s → ~1s initially.
		done <- l.Wait(context.Background(), 2048)
	}()

	time.Sleep(200 * time.Millisecond)
	// Drop rate to 1 KB/s — remaining wait should slow down.
	l.Update(1000)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// Started at 2 KB/s, after 200ms dropped to 1 KB/s.
	// Approx: 0.2s at 2 KB/s = 0.4 KB done, 1.6 KB left at 1 KB/s = 1.6s.
	// Total ≈ 1.8s. Accept 1.2s–2.5s.
	t.Logf("elapsed: %v", elapsed)
	if elapsed < 1200*time.Millisecond {
		t.Fatalf("expected >1.2s after rate drop, got %v", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("expected <2.5s, got %v (too slow)", elapsed)
	}
}
