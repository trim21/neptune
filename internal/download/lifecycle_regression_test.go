// Copyright 2026 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/stretchr/testify/require"

	"neptune/internal/client/tracker"
	"neptune/internal/piece_store"
	"neptune/internal/pkg/bm"
)

type lifecycleRoundTripper func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestRunHashCheckFailureReleasesCompletedOnce is a regression test: when a
// completion recheck (recheckAfterComplete) fails during initCheck, the
// completedOnce guard must be released. Otherwise a later completion via
// checkDone is blocked forever by the CompareAndSwap.
func TestRunHashCheckFailureReleasesCompletedOnce(t *testing.T) {
	d := newTestDownload(t, 2, 4, piece_store.NewMemStore)

	// d.s.basePath is empty in the test harness, so initCheck fails in
	// CheckExistingFiles (os.MkdirAll("")).
	d.completedOnce.Store(true)

	d.runHashCheck(nil)

	require.Eventually(t, func() bool {
		return !d.completedOnce.Load() && d.ErrorMsg() != ""
	}, 5*time.Second, 5*time.Millisecond, "completedOnce must be released after initCheck failure")
}

func TestCompletionRecheckAnnouncesCompletedWithoutStarted(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	f.writeDataFile(t)
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.info = f.info
	d.store = piece_store.NewMemStore(d.info)
	d.selectedFilesSet = bm.New(uint32(len(d.info.Files)))
	d.selectedFilesSet.Fill()
	d.s.basePath = f.basePath
	d.completedBm.Fill()
	d.missingBm.Clear()
	d.completed.Store(d.info.TotalLength)
	d.completedOnce.Store(true)

	requests := make(chan *http.Request, 2)
	httpClient := resty.New().SetTransport(lifecycleRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("d8:intervali1800e5:peers0:e")),
			Request:    req,
		}, nil
	}))
	d.tracker = tracker.New(d.ctx, tracker.Config{
		HTTP:       httpClient,
		Uploaded:   &d.uploaded,
		Downloaded: &d.downloaded,
		Completed:  &d.completed,
		TotalSize:  d.info.TotalLength,
	})
	d.tracker.SetTiers([]tracker.TrackerTier{{Trackers: []*tracker.Tracker{{
		URL:          "http://tracker.test/announce",
		NextAnnounce: time.Now(),
	}}}})
	d.tracker.Start(0)

	// Send the initial started event first. The completion recheck must append
	// completed without creating another started event of its own.
	done := make(chan struct{})
	go func() {
		d.tracker.Run()
		close(done)
	}()
	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventStarted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("initial started announce was not issued")
	}

	d.recheckAfterComplete()
	require.Eventually(t, func() bool {
		return d.HasState(Seeding) && d.picker.Load() == nil
	}, 5*time.Second, 5*time.Millisecond)

	defer func() {
		d.cancel()
		<-done
	}()

	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventCompleted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("completed announce was not issued")
	}
	select {
	case req := <-requests:
		t.Fatalf("unexpected additional announce event %q", req.URL.Query().Get("event"))
	case <-time.After(100 * time.Millisecond):
	}
}

// newMockTracker wires d.tracker with a mock HTTP transport that records every
// announce request and returns a benign 30-minute-interval response.
func newMockTracker(d *Download, requests chan<- *http.Request) {
	httpClient := resty.New().SetTransport(lifecycleRoundTripper(func(req *http.Request) (*http.Response, error) {
		requests <- req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("d8:intervali1800e5:peers0:e")),
			Request:    req,
		}, nil
	}))
	d.tracker = tracker.New(d.ctx, tracker.Config{
		HTTP:       httpClient,
		Uploaded:   &d.uploaded,
		Downloaded: &d.downloaded,
		Completed:  &d.completed,
		TotalSize:  d.info.TotalLength,
	})
	d.tracker.SetTiers([]tracker.TrackerTier{{Trackers: []*tracker.Tracker{{
		URL: "http://tracker.test/announce",
	}}}})
}

// TestManualRecheckCompleteDoesNotAnnounce verifies that a manual recheck
// (AsyncCheck) of an active download whose data verifies cleanly does not
// re-announce: the chain stays active on its own schedule.
func TestManualRecheckCompleteDoesNotAnnounce(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	f.writeDataFile(t)
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.info = f.info
	d.store = piece_store.NewMemStore(d.info)
	d.selectedFilesSet = bm.New(uint32(len(d.info.Files)))
	d.selectedFilesSet.Fill()
	d.s.basePath = f.basePath

	requests := make(chan *http.Request, 2)
	newMockTracker(d, requests)

	done := make(chan struct{})
	go func() {
		d.tracker.Run()
		close(done)
	}()
	defer func() {
		d.cancel()
		<-done
	}()

	// Activate the announce chain; the next periodic announce now sits in the
	// future (the mock response advertises a 30-minute interval).
	d.tracker.Start(0)
	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventStarted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("started announce was not issued")
	}

	require.NoError(t, d.AsyncCheck())
	require.Eventually(t, func() bool {
		return d.HasState(Seeding) && d.picker.Load() == nil
	}, 5*time.Second, 5*time.Millisecond)

	select {
	case req := <-requests:
		t.Fatalf("manual recheck of complete data must not announce, got event %q", req.URL.Query().Get("event"))
	case <-time.After(300 * time.Millisecond):
	}
}

// TestManualRecheckIncompleteAnnouncesStarted verifies that a manual recheck
// of a stopped torrent finding missing/corrupt pieces returns it to
// Downloading; syncTrackerState starts the announce chain (event=started) so
// the swarm can supply peers for the missing pieces.
func TestManualRecheckIncompleteAnnouncesStarted(t *testing.T) {
	f := newResumeTestFixture(t, 1)
	// Write corrupt data: the piece digest is SHA-1 of zeroes, so all-0xFF
	// content fails verification and leaves the torrent incomplete.
	path := filepath.Join(f.basePath, f.info.Files[0].Path)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{0xFF}, int(f.info.TotalLength)), 0o644))

	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	d.info = f.info
	d.store = piece_store.NewMemStore(d.info)
	d.selectedFilesSet = bm.New(uint32(len(d.info.Files)))
	d.selectedFilesSet.Fill()
	d.s.basePath = f.basePath
	d.state.Store(uint32(Stopped))

	requests := make(chan *http.Request, 2)
	newMockTracker(d, requests)

	require.NoError(t, d.AsyncCheck())
	require.Eventually(t, func() bool {
		return d.HasState(Downloading)
	}, 5*time.Second, 5*time.Millisecond)

	done := make(chan struct{})
	go func() {
		d.tracker.Run()
		close(done)
	}()
	defer func() {
		d.cancel()
		<-done
	}()

	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventStarted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("failed recheck did not re-register with trackers")
	}
}

// TestErrorStateAnnouncesStopped verifies that entering the Error state
// stops the announce chain: syncTrackerState sends stopped instead of letting
// an errored download keep announcing itself as online.
func TestErrorStateAnnouncesStopped(t *testing.T) {
	d := newTestDownload(t, 1, 4, piece_store.NewMemStore)
	requests := make(chan *http.Request, 2)
	newMockTracker(d, requests)

	done := make(chan struct{})
	go func() {
		d.tracker.Run()
		close(done)
	}()
	defer func() {
		d.cancel()
		<-done
	}()

	d.tracker.Start(0)
	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventStarted), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("started announce was not issued")
	}

	d.setError(errors.New("test error"))
	select {
	case req := <-requests:
		require.Equal(t, string(tracker.EventStopped), req.URL.Query().Get("event"))
	case <-time.After(time.Second):
		t.Fatal("error state did not announce stopped")
	}
}
