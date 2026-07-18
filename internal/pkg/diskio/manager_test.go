// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build linux

package diskio

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newTestQueue(t *testing.T, workers, queueOps int, queuedBytes int64) *Queue {
	t.Helper()
	q := newQueueWithProfile(
		DeviceID{Major: 1, Minor: 2},
		DeviceHDD,
		profile{workers: workers, queueOps: queueOps, queuedBytes: queuedBytes},
		newMetrics(),
	)
	t.Cleanup(q.Close)
	return q
}

func TestQueueDoWaitsForCompletion(t *testing.T) {
	q := newTestQueue(t, 1, 1, 1024)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- q.Do(context.Background(), ClassWrite, 10, func() error {
			close(started)
			<-release
			return nil
		})
	}()

	<-started
	select {
	case err := <-done:
		t.Fatalf("Do returned before completion: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueueDoDrainsPooledResultChannel(t *testing.T) {
	q := newTestQueue(t, 1, 1, 1024)
	want := errors.New("write failed")
	if err := q.Do(context.Background(), ClassWrite, 1, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("first Do error = %v, want %v", err, want)
	}
	if err := q.Do(context.Background(), ClassWrite, 1, func() error { return nil }); err != nil {
		t.Fatalf("second Do received stale pooled result: %v", err)
	}
}

func TestQueueBoundsConcurrency(t *testing.T) {
	q := newTestQueue(t, 2, 4, 1024)
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	done := make(chan error, 3)

	for range 3 {
		go func() {
			done <- q.Do(context.Background(), ClassRead, 1, func() error {
				started <- struct{}{}
				<-release
				return nil
			})
		}()
	}

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third operation started above the worker limit")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("third operation did not start after capacity was released")
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueueBoundsAdmittedBytes(t *testing.T) {
	q := newTestQueue(t, 1, 2, 10)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- q.Do(context.Background(), ClassWrite, 10, func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := q.Do(ctx, ClassWrite, 1, func() error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestQueueCloseDrainsAcceptedOperations(t *testing.T) {
	q := newTestQueue(t, 1, 2, 1024)
	release := make(chan struct{})
	started := make(chan struct{})
	done := make(chan error, 2)
	var completed atomic.Int32

	go func() {
		done <- q.Do(context.Background(), ClassRead, 1, func() error {
			close(started)
			<-release
			completed.Add(1)
			return nil
		})
	}()
	<-started
	go func() {
		done <- q.Do(context.Background(), ClassRead, 1, func() error {
			completed.Add(1)
			return nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for len(q.jobs) != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(q.jobs) != 1 {
		t.Fatal("second operation was not admitted before Close")
	}

	closed := make(chan struct{})
	go func() {
		q.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned with an operation in flight")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-closed
	if got := completed.Load(); got != 2 {
		t.Fatalf("completed = %d, want 2", got)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Do(context.Background(), ClassRead, 1, func() error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("Do after Close error = %v, want %v", err, ErrClosed)
	}
}

func TestManagerGroupsQueuesByDevice(t *testing.T) {
	oldPath := discoverPath
	oldDevices := discoverDevices
	discoverDevices = nil
	discoverPath = func(path string) (deviceInfo, bool) {
		if path == "/ssd" {
			return deviceInfo{id: DeviceID{Major: 8, Minor: 1}, class: DeviceSSD}, true
		}
		return deviceInfo{id: DeviceID{Major: 8, Minor: 2}, class: DeviceHDD}, true
	}
	t.Cleanup(func() {
		discoverPath = oldPath
		discoverDevices = oldDevices
	})

	m := New()
	t.Cleanup(m.Close)
	ssd := m.QueueForPath("/ssd")
	if ssd != m.QueueForPath("/ssd") {
		t.Fatal("same device did not reuse its queue")
	}
	if ssd == m.QueueForPath("/hdd") {
		t.Fatal("different devices shared a queue")
	}
	if ssd.devClass != DeviceSSD.String() {
		t.Fatalf("SSD queue class = %q", ssd.devClass)
	}
}
