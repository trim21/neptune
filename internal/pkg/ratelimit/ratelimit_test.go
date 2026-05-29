// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package ratelimit

import (
	"context"
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

	start := time.Now()
	err := l.Wait(context.Background(), 256*1024)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("first call should not block significantly")
	}

	start = time.Now()
	err = l.Wait(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 500*time.Millisecond {
		t.Fatalf("expected ~1s block, got %v", elapsed)
	}
}

func TestLimiterContextCancel(t *testing.T) {
	l := New(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := l.Wait(ctx, 256*1024+1)
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestUpdateToUnlimitedWhileWaiting(t *testing.T) {
	l := New(1)

	done := make(chan error, 1)
	go func() {
		done <- l.Wait(context.Background(), 256*1024+1)
	}()

	time.Sleep(50 * time.Millisecond)
	l.Update(0)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after Update(0)")
	}
}
