// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tracker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func successResponse(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("d8:intervali1800e5:peers0:e")),
		Request:    req,
	}
}

func newTestTrackers(ctx context.Context, transport http.RoundTripper) *Trackers {
	return New(ctx, Config{
		HTTP:       resty.New().SetTransport(transport),
		Uploaded:   atomic.NewInt64(0),
		Downloaded: atomic.NewInt64(0),
		Completed:  atomic.NewInt64(0),
	})
}

// TestEarliestNextAnnounceWhileInflight verifies that the loop must not arm
// its timer on the stale NextAnnounce of an in-flight round: the times that
// started the round stay expired until applyAnnounceResult runs, so a timer
// set on them fires immediately and the loop busy-spins for the whole HTTP
// request. While a round is in flight the loop sleeps on the wake channel
// instead (finishRound always wakes it).
func TestEarliestNextAnnounceWhileInflight(t *testing.T) {
	trackers := New(context.Background(), Config{})
	due := time.Now().Add(-time.Minute) // stale: the in-flight round started at this time
	tr := &Tracker{URL: "http://tracker.test/announce", NextAnnounce: due}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})
	trackers.mu.Lock()
	trackers.inFlight = true
	trackers.mu.Unlock()

	next, ok := trackers.earliestNextAnnounce()
	require.False(t, ok, "loop must not arm a timer on the stale NextAnnounce while a round is in-flight")
	require.True(t, next.IsZero())
}

// TestSchedulerRunsRegularRoundAtDueTime verifies that a regular (no-event)
// round fires when a tracker's NextAnnounce expires.
func TestSchedulerRunsRegularRoundAtDueTime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 1)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return successResponse(req), nil
	}))
	due := time.Now().Add(75 * time.Millisecond)
	tr := &Tracker{URL: "http://tracker.test/announce", NextAnnounce: due}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})
	trackers.mu.Lock()
	trackers.active = true
	trackers.mu.Unlock()

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case req := <-requests:
		require.False(t, time.Now().Before(due), "regular round fired before NextAnnounce")
		require.Empty(t, req.URL.Query().Get("event"), "regular round must carry no event")
	case <-time.After(time.Second):
		t.Fatal("regular announce was not scheduled at NextAnnounce")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight && tr.NextAnnounce.After(time.Now())
	}, time.Second, time.Millisecond)
}

func TestStartSchedulesStartedWithinStaggerWindow(t *testing.T) {
	trackers := New(context.Background(), Config{})
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{
		{URL: "http://tracker.test/1"},
		{URL: "http://tracker.test/2"},
	}}})
	before := time.Now()
	trackers.Start(30 * time.Second)
	after := time.Now().Add(30 * time.Second)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.True(t, trackers.active)
	require.Equal(t, EventStarted, trackers.pendingEvent)
	require.False(t, trackers.pendingAt.Before(before))
	require.True(t, trackers.pendingAt.Before(after))
}

// TestAnnounceWhileInflight verifies that an event enqueued while a round is
// in flight is not pushed out to the next interval: the completing round must
// hand the newer event back to the loop promptly.
func TestAnnounceWhileInflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	requests := make(chan *http.Request, 10)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		<-block
		return successResponse(req), nil
	}))
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// First started round starts immediately and is blocked in the transport.
	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("first announce did not start")
	}

	trackers.Announce(EventCompleted)

	// Release the in-flight round; the external event must fire promptly,
	// not at the next 30-minute interval.
	close(block)
	select {
	case req := <-requests:
		require.Equal(t, string(EventCompleted), req.URL.Query().Get("event"))
	case <-time.After(2 * time.Second):
		t.Fatal("external announce was not issued promptly")
	}
}

func TestStartWhileStoppedAnnounceIsInflight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	releaseStop := make(chan struct{})
	var releaseStopOnce sync.Once
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if req.URL.Query().Get("event") == string(EventStopped) {
			<-releaseStop
		}
		return successResponse(req), nil
	}))
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})
	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		releaseStopOnce.Do(func() { close(releaseStop) })
		cancel()
		<-done
	}()

	trackers.Start(0)
	require.Equal(t, string(EventStarted), (<-requests).URL.Query().Get("event"))
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	trackers.Announce(EventStopped)
	require.Equal(t, string(EventStopped), (<-requests).URL.Query().Get("event"))
	trackers.Start(0)
	releaseStopOnce.Do(func() { close(releaseStop) })

	select {
	case req := <-requests:
		require.Equal(t, string(EventStarted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("started announce did not follow the in-flight stopped announce")
	}
}

func TestJitterIntervalRange(t *testing.T) {
	interval := 30 * time.Minute
	minDelta := 15 * time.Minute

	lo := 0.85 * float64(interval)
	hi := 1.15 * float64(interval)

	var hitLo, hitHi bool
	for range 2000 {
		got := jitterInterval(interval, minDelta)
		g := float64(got)
		assert.GreaterOrEqual(t, g, lo, "must not jitter below -15%%")
		assert.LessOrEqual(t, g, hi, "must not jitter above +15%%")
		if g < lo+float64(time.Minute) {
			hitLo = true
		}
		if g > hi-float64(time.Minute) {
			hitHi = true
		}
	}

	assert.True(t, hitLo, "jitter should reach the lower bound")
	assert.True(t, hitHi, "jitter should reach the upper bound")
}

func TestJitterIntervalClampsToMinDelta(t *testing.T) {
	// When minDelta sits above the lower jitter bound, the result must be
	// clamped so the tracker's min_interval is never violated.
	interval := 10 * time.Minute
	minDelta := 9 * time.Minute // > 0.85*interval = 8.5min

	for range 1000 {
		got := jitterInterval(interval, minDelta)
		assert.GreaterOrEqual(t, got, minDelta, "must never announce before min_interval")
		assert.LessOrEqual(t, got, time.Duration(1.15*float64(interval)), "must not jitter above +15%%")
	}
}

func TestLifecycleEventsCoalesceToLatestState(t *testing.T) {
	trackers := New(context.Background(), Config{})
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{{URL: "http://tracker.test/announce"}}}})

	trackers.Start(0)
	trackers.Announce(EventCompleted)
	trackers.Announce(EventStopped)
	trackers.Start(0)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.True(t, trackers.active)
	require.True(t, trackers.pendingCompleted)
	require.Equal(t, EventStarted, trackers.pendingEvent)
}

func TestImmediateLifecycleEventCoalescesStaggeredState(t *testing.T) {
	trackers := New(context.Background(), Config{})
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{{URL: "http://tracker.test/announce"}}}})

	trackers.Start(30 * time.Second)
	trackers.Announce(EventStopped)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.False(t, trackers.active)
	require.Equal(t, EventStopped, trackers.pendingEvent)
	require.False(t, trackers.pendingCompleted)
	require.WithinDuration(t, time.Now(), trackers.pendingAt, time.Second)
}

func TestCompletedPreservesStoppedState(t *testing.T) {
	trackers := New(context.Background(), Config{})
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{{URL: "http://tracker.test/announce"}}}})

	trackers.Start(0)
	trackers.Announce(EventStopped)
	trackers.Announce(EventCompleted)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.False(t, trackers.active)
	require.True(t, trackers.pendingCompleted)
	require.Equal(t, EventStopped, trackers.pendingEvent)
}

func TestCompletedAdvancesStaggeredStarted(t *testing.T) {
	trackers := New(context.Background(), Config{})
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{{URL: "http://tracker.test/announce"}}}})

	trackers.Start(30 * time.Second)
	trackers.Announce(EventCompleted)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.True(t, trackers.active)
	require.True(t, trackers.pendingCompleted)
	require.Equal(t, EventStarted, trackers.pendingEvent)
	require.WithinDuration(t, time.Now(), trackers.pendingAt, time.Second)
}

func TestLifecycleEventsPreserveCompletionBeforeLatestState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 8)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return successResponse(req), nil
	}))
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{{
		URL: "http://tracker.test/announce",
	}}}})

	trackers.Start(0)
	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	select {
	case req := <-requests:
		require.Equal(t, string(EventStarted), req.URL.Query().Get("event"))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for started")
	}

	trackers.Announce(EventCompleted)
	trackers.Announce(EventStopped)
	trackers.Start(0)

	for _, want := range []AnnounceEvent{EventCompleted, EventStarted} {
		select {
		case req := <-requests:
			require.Equal(t, string(want), req.URL.Query().Get("event"))
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %q", want)
		}
	}
}

// TestReannounceDoesNotOverridePendingLifecycle verifies that a manual regular
// announce cannot replace a lifecycle event during the resume stagger window.
func TestReannounceDoesNotOverridePendingLifecycle(t *testing.T) {
	trackers := New(context.Background(), Config{})
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})

	trackers.Start(30 * time.Second) // schedules started in the future

	trackers.mu.RLock()
	require.Equal(t, EventStarted, trackers.pendingEvent)
	trackers.mu.RUnlock()

	require.False(t, trackers.Reannounce())

	trackers.mu.RLock()
	require.Equal(t, EventStarted, trackers.pendingEvent, "reannounce must not consume pending lifecycle event")
	trackers.mu.RUnlock()
}

// TestManualReannounceSendsRegularRound verifies that Reannounce fires a
// regular round once a tracker reached its EarliestAnnounce.
func TestManualReannounceSendsRegularRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return successResponse(req), nil
	}))
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("started announce did not fire")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	trackers.mu.Lock()
	tr.EarliestAnnounce = time.Now().Add(-time.Second)
	trackers.mu.Unlock()

	require.True(t, trackers.Reannounce())
	select {
	case req := <-requests:
		require.Empty(t, req.URL.Query().Get("event"), "manual reannounce must carry no event")
	case <-time.After(2 * time.Second):
		t.Fatal("manual reannounce did not fire")
	}
}

// TestShutdownSendsStoppedForInflightTracker verifies that Shutdown sends
// stopped even to a tracker whose request is currently in flight instead of
// skipping it.
func TestShutdownSendsStoppedForInflightTracker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if req.URL.Query().Get("event") == string(EventStarted) {
			<-block
		}
		return successResponse(req), nil
	}))
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		close(block)
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("first announce did not start")
	}

	// The started announce is now blocked in flight. Shutdown must still send
	// stopped to this tracker instead of skipping it.
	trackers.Shutdown()
	select {
	case req := <-requests:
		require.Equal(t, string(EventStopped), req.URL.Query().Get("event"))
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not send stopped for in-flight tracker")
	}
}

// TestTierFailoverBackupUsedWhenPrimaryFails verifies BEP 12 failover: when
// tier 0 fails entirely, the round advances to tier 1.
func TestTierFailoverBackupUsedWhenPrimaryFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if strings.Contains(req.URL.String(), "tracker.test/1") {
			return nil, errors.New("connection refused")
		}
		return successResponse(req), nil
	}))
	tr1 := &Tracker{URL: "http://tracker.test/1"}
	tr2 := &Tracker{URL: "http://tracker.test/2"}
	trackers.SetTiers([]TrackerTier{
		{Trackers: []*Tracker{tr1}},
		{Trackers: []*Tracker{tr2}},
	})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, 2*time.Second, time.Millisecond)

	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return tr1.Err != nil && tr2.Err == nil
	}, time.Second, time.Millisecond)
}

// TestTierFailoverBackupUntouchedWhenPrimaryWorks verifies BEP 12: when tier 0
// succeeds, the backup tier is left untouched.
func TestTierFailoverBackupUntouchedWhenPrimaryWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	backupHits := atomic.NewInt32(0)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if strings.Contains(req.URL.String(), "tracker.test/backup") {
			backupHits.Inc()
		}
		return successResponse(req), nil
	}))
	tr1 := &Tracker{URL: "http://tracker.test/primary"}
	tr2 := &Tracker{URL: "http://tracker.test/backup"}
	trackers.SetTiers([]TrackerTier{
		{Trackers: []*Tracker{tr1}},
		{Trackers: []*Tracker{tr2}},
	})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("primary announce did not fire")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	require.Zero(t, backupHits.Load(), "backup tier must not be announced while tier 0 works")
}

// TestStoppedBroadcastOnlyToAttemptedTrackers verifies that the stopped round
// reaches only trackers that ever received a request.
func TestStoppedBroadcastOnlyToAttemptedTrackers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return successResponse(req), nil
	}))
	tr1 := &Tracker{URL: "http://tracker.test/primary"}
	tr2 := &Tracker{URL: "http://tracker.test/backup"}
	trackers.SetTiers([]TrackerTier{
		{Trackers: []*Tracker{tr1}},
		{Trackers: []*Tracker{tr2}},
	})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("primary announce did not fire")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	trackers.Announce(EventStopped)
	select {
	case req := <-requests:
		require.Equal(t, string(EventStopped), req.URL.Query().Get("event"))
		require.Contains(t, req.URL.String(), "tracker.test/primary")
	case <-time.After(2 * time.Second):
		t.Fatal("stopped announce did not fire")
	}

	// The backup was never announced; a second stopped request must not arrive.
	select {
	case req := <-requests:
		t.Fatalf("unexpected request to backup tracker: %s", req.URL.String())
	case <-time.After(100 * time.Millisecond):
	}
}

// TestBackupNotDueDoesNotDriveRegularRound verifies that a backup tier's
// expiry never starts a regular round while the leading tier is alive: the
// leading tier alone drives the rhythm (BEP 12 failover).
func TestBackupNotDueDoesNotDriveRegularRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	backupHits := atomic.NewInt32(0)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if strings.Contains(req.URL.String(), "tracker.test/backup") {
			backupHits.Inc()
		}
		return successResponse(req), nil
	}))
	tr1 := &Tracker{URL: "http://tracker.test/primary"}
	tr2 := &Tracker{URL: "http://tracker.test/backup", NextAnnounce: time.Now()}
	trackers.SetTiers([]TrackerTier{
		{Trackers: []*Tracker{tr1}},
		{Trackers: []*Tracker{tr2}},
	})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case req := <-requests:
		require.Contains(t, req.URL.String(), "tracker.test/primary")
	case <-time.After(2 * time.Second):
		t.Fatal("primary announce did not fire")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	require.Zero(t, backupHits.Load(), "backup tier must not be announced while tier 0 is alive")

	// The loop must stay responsive (no busy loop) and still deliver a stopped
	// round afterwards.
	trackers.Announce(EventStopped)
	select {
	case req := <-requests:
		require.Equal(t, string(EventStopped), req.URL.Query().Get("event"))
	case <-time.After(2 * time.Second):
		t.Fatal("stopped announce did not fire after backup-expiry round")
	}
}

// TestSuccessfulTrackerMovedToFrontOfTier verifies BEP 12: a successful
// tracker is moved to the front of its tier.
func TestSuccessfulTrackerMovedToFrontOfTier(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	requests := make(chan *http.Request, 4)
	trackers := newTestTrackers(ctx, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests <- req
		if strings.Contains(req.URL.String(), "tracker.test/1") {
			return nil, errors.New("connection refused")
		}
		return successResponse(req), nil
	}))
	tr1 := &Tracker{URL: "http://tracker.test/1"}
	tr2 := &Tracker{URL: "http://tracker.test/2"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr1, tr2}}})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	trackers.Start(0)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("started announce did not fire")
	}
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight && len(trackers.tiers) == 1 &&
			len(trackers.tiers[0].Trackers) == 2 && trackers.tiers[0].Trackers[0] == tr2
	}, 2*time.Second, time.Millisecond)

	trackers.mu.RLock()
	defer trackers.mu.RUnlock()
	require.Len(t, trackers.tiers, 1)
	require.Len(t, trackers.tiers[0].Trackers, 2)
	require.Same(t, tr2, trackers.tiers[0].Trackers[0], "successful tracker must move to front of tier")
}

// TestRemovedTrackerLateResponseNoSideEffects verifies that a late response
// from a tracker removed while its request was in flight does not repopulate
// deleted swarm statistics.
func TestRemovedTrackerLateResponseNoSideEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	block := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(block) }) }
	httpClient := resty.New().SetTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("event") != string(EventStarted) {
			return successResponse(req), nil
		}
		<-block
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("d8:completei5e8:intervali1800e8:incompletei3e5:peers0:e")),
			Request:    req,
		}, nil
	}))
	trackers := New(ctx, Config{
		HTTP:       httpClient,
		Uploaded:   atomic.NewInt64(0),
		Downloaded: atomic.NewInt64(0),
		Completed:  atomic.NewInt64(0),
	})
	tr := &Tracker{URL: "http://tracker.test/announce"}
	trackers.SetTiers([]TrackerTier{{Trackers: []*Tracker{tr}}})

	done := make(chan struct{})
	go func() {
		trackers.Run()
		close(done)
	}()
	defer func() {
		release()
		cancel()
		<-done
	}()

	trackers.Start(0)
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return trackers.inFlight
	}, time.Second, time.Millisecond)

	trackers.Remove(tr.URL)
	release()
	require.Eventually(t, func() bool {
		trackers.mu.RLock()
		defer trackers.mu.RUnlock()
		return !trackers.inFlight
	}, time.Second, time.Millisecond)

	_, ok := trackers.Seeds.Load(tr.URL)
	require.False(t, ok, "late response repopulated seeders for removed tracker")
	_, ok = trackers.Leechers.Load(tr.URL)
	require.False(t, ok, "late response repopulated leechers for removed tracker")
	_, ok = trackers.Errors.Load(tr.URL)
	require.False(t, ok, "late response repopulated errors for removed tracker")
}
