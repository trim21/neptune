// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package disk_io

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type executorFunc func(context.Context, Operation) Result

func (fn executorFunc) Execute(ctx context.Context, op Operation) Result {
	return fn(ctx, op)
}

func successfulExecutor(_ context.Context, op Operation) Result {
	return Result{N: int(op.operationInfo().bytes)}
}

func writeOperation(bytes int) PWrite {
	return PWrite{Buffer: make([]byte, bytes)}
}

func readOperation(bytes int) PRead {
	return PRead{Buffer: make([]byte, bytes)}
}

func newTestQueue(t *testing.T, workers, queueOps int, queuedBytes int64, executor Executor) *Queue {
	t.Helper()
	if executor == nil {
		executor = executorFunc(successfulExecutor)
	}
	q := newQueueWithProfile(
		DeviceID{Major: 1, Minor: 2},
		DeviceHDD,
		profile{workers: workers, queueOps: queueOps, queuedBytes: queuedBytes},
		executor,
		newMetrics(),
	)
	t.Cleanup(q.Close)
	return q
}

func TestQueueDoWaitsForCompletion(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	q := newTestQueue(t, 1, 1, 1024, executorFunc(func(_ context.Context, op Operation) Result {
		close(started)
		<-release
		return successfulExecutor(context.Background(), op)
	}))
	done := make(chan Result, 1)

	go func() {
		done <- q.Do(context.Background(), writeOperation(10))
	}()

	<-started
	select {
	case result := <-done:
		t.Fatalf("Do returned before completion: %+v", result)
	default:
	}
	close(release)
	result := <-done
	if result.Err != nil || result.N != 10 {
		t.Fatalf("Do result = %+v", result)
	}
}

func TestQueueDoDrainsPooledResultChannel(t *testing.T) {
	want := errors.New("write failed")
	var calls atomic.Int32
	q := newTestQueue(t, 1, 1, 1024, executorFunc(func(_ context.Context, _ Operation) Result {
		if calls.Add(1) == 1 {
			return Result{Err: want}
		}
		return Result{N: 1}
	}))

	if result := q.Do(context.Background(), writeOperation(1)); !errors.Is(result.Err, want) {
		t.Fatalf("first Do result = %+v, want error %v", result, want)
	}
	if result := q.Do(context.Background(), writeOperation(1)); result.Err != nil || result.N != 1 {
		t.Fatalf("second Do received stale pooled result: %+v", result)
	}
}

func TestQueuePassesStructuredOperationToExecutor(t *testing.T) {
	want := PRead{Buffer: make([]byte, 4), Offset: 42}
	seen := make(chan Operation, 1)
	q := newTestQueue(t, 1, 1, 1024, executorFunc(func(_ context.Context, op Operation) Result {
		seen <- op
		return Result{N: 4}
	}))

	result := q.Do(context.Background(), want)
	if result.Err != nil || result.N != 4 {
		t.Fatalf("Do result = %+v", result)
	}
	got, ok := (<-seen).(PRead)
	if !ok || got.Offset != want.Offset || len(got.Buffer) != len(want.Buffer) {
		t.Fatalf("executor operation = %#v", got)
	}
}

func TestQueueBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	q := newTestQueue(t, 2, 4, 1024, executorFunc(func(_ context.Context, op Operation) Result {
		started <- struct{}{}
		<-release
		return successfulExecutor(context.Background(), op)
	}))
	done := make(chan Result, 3)

	for range 3 {
		go func() {
			done <- q.Do(context.Background(), readOperation(1))
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
		if result := <-done; result.Err != nil {
			t.Fatal(result.Err)
		}
	}
}

func TestQueueBoundsAdmittedBytes(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	q := newTestQueue(t, 1, 2, 10, executorFunc(func(_ context.Context, op Operation) Result {
		close(started)
		<-release
		return successfulExecutor(context.Background(), op)
	}))
	done := make(chan Result, 1)
	go func() {
		done <- q.Do(context.Background(), writeOperation(10))
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := q.Do(ctx, writeOperation(1))
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Do result = %+v, want deadline exceeded", result)
	}
	close(release)
	if result = <-done; result.Err != nil {
		t.Fatal(result.Err)
	}
}

func TestQueueCloseDrainsAcceptedOperations(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	q := newTestQueue(t, 1, 2, 1024, executorFunc(func(_ context.Context, op Operation) Result {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return successfulExecutor(context.Background(), op)
	}))
	done := make(chan Result, 2)

	go func() { done <- q.Do(context.Background(), readOperation(1)) }()
	<-started
	go func() { done <- q.Do(context.Background(), readOperation(1)) }()
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
	if got := calls.Load(); got != 2 {
		t.Fatalf("executed operations = %d, want 2", got)
	}
	for range 2 {
		if result := <-done; result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if result := q.Do(context.Background(), readOperation(1)); !errors.Is(result.Err, ErrClosed) {
		t.Fatalf("Do after Close result = %+v, want %v", result, ErrClosed)
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

	m := New(executorFunc(successfulExecutor))
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
