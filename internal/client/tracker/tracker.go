// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package tracker manages BitTorrent tracker announce loops.
package tracker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/atomic"
	"golang.org/x/sync/semaphore"
)

type AnnounceEvent string

const (
	EventStarted   AnnounceEvent = "started"
	EventCompleted AnnounceEvent = "completed"
	EventStopped   AnnounceEvent = "stopped"
)

// DiscoveredPeer holds a peer address with its discovery source.
type DiscoveredPeer struct {
	AddrPort netip.AddrPort
	Source   PeerSource
}

// PeerSource mirrors libtorrent's peer_source_flags_t.
type PeerSource uint8

const (
	PeerSourceTracker    PeerSource = 1 << 0
	PeerSourceDHT        PeerSource = 1 << 1
	PeerSourcePEX        PeerSource = 1 << 2
	PeerSourceLSD        PeerSource = 1 << 3
	PeerSourceResumeData PeerSource = 1 << 4
	PeerSourceIncoming   PeerSource = 1 << 5
)

// SourceRank returns a priority score for a peer source.
// Higher rank = higher priority for connection.
// Mirrors libtorrent's source_rank().
func SourceRank(source PeerSource) int {
	r := 0
	if source&PeerSourceTracker != 0 {
		r |= 1 << 5
	}
	if source&PeerSourceLSD != 0 {
		r |= 1 << 4
	}
	if source&PeerSourceDHT != 0 {
		r |= 1 << 3
	}
	if source&PeerSourcePEX != 0 {
		r |= 1 << 2
	}
	return r
}

// AnnounceResponse is the parsed result from a tracker announce.
// Interval and MinInterval are zero if the tracker did not return them;
// the caller (applyAnnounceResult) applies the merging rules.
// LeechersKnown is true when the tracker response explicitly included an
// "incomplete" field (Leechers is authoritative, including zero).
type AnnounceResponse struct {
	Err           error
	FailedReason  string
	Peers         []netip.AddrPort
	Interval      time.Duration
	MinInterval   time.Duration
	Seeders       int
	Leechers      int
	LeechersKnown bool
}

// Tracker is a single announce URL with its own throttling state.
// Scheduling itself is group-level (see Trackers); a Tracker only records the
// times after which it may be announced again and its last result.
type Tracker struct {
	LastAnnounceTime time.Time
	// NextAnnounce is the earliest time a regular (no-event) round may try
	// this tracker again. It is renewed after every attempt by the response
	// interval (or the failure retry interval).
	NextAnnounce time.Time
	// EarliestAnnounce is the earliest time a manual reannounce may try this
	// tracker (min_interval). It is only used for Reannounce throttling.
	EarliestAnnounce time.Time
	Err              error
	URL              string
	FailureMessage   string
	Interval         time.Duration
	PeerCount        int
	// everAttempted marks that a request has been sent to this tracker at
	// least once. It defines the set of trackers that receive stopped on
	// shutdown: a tracker that never got a request has no record to clear.
	everAttempted atomic.Bool
}

// ErrorMessage returns the current error state description.
func (t *Tracker) ErrorMessage() string {
	if t.FailureMessage != "" {
		return t.FailureMessage
	}
	if t.Err != nil {
		return t.Err.Error()
	}
	return ""
}

// TrackerTier is a group of trackers at the same priority tier.
type TrackerTier struct {
	Trackers []*Tracker
}

// Config holds the static configuration for Trackers.
type Config struct {
	Log             zerolog.Logger
	PeersCh         chan<- []DiscoveredPeer
	TrackerSem      *semaphore.Weighted
	Uploaded        *atomic.Int64
	Downloaded      *atomic.Int64
	Completed       *atomic.Int64
	HTTP            *resty.Client
	Key             string
	InfoHash        string
	PeerID          string
	TotalSize       int64
	UploadedStart   int64
	DownloadedStart int64
	NumWant         int32
	Port            uint16
	Debug           bool
}

// Trackers manages announce trackers and the background announce loop.
//
// Scheduling is group-level: every announce (a lifecycle event or a regular
// round) walks the tiers in order (BEP 12). A round first tries every tracker
// in tier 0 in parallel; when at least one succeeds the round ends and the
// backup tiers are left untouched. Only when the whole tier fails does the
// round advance to the next tier. The next regular round is driven by the
// per-tracker NextAnnounce times (renewed by each response interval), so a
// tracker that is never reached (a working backup) simply never expires.
type Trackers struct {
	log zerolog.Logger
	// pendingAt is the earliest time pendingEvent may be dispatched. A zero
	// time means "as soon as possible"; Start(maxDelay) staggers it.
	pendingAt time.Time
	ctx       context.Context
	Errors    *xsync.Map[string, string]
	Seeds     *xsync.Map[string, int]
	Leechers  *xsync.Map[string, int]
	uploaded  *atomic.Int64
	wakeCh    chan struct{}
	peersCh   chan<- []DiscoveredPeer
	http      *resty.Client
	completed *atomic.Int64

	trackerSem *semaphore.Weighted
	downloaded *atomic.Int64
	infoHash   string

	inFlightEvent AnnounceEvent
	peerID        string
	// pendingEvent is the latest desired lifecycle state (started/stopped)
	// that has not been sent yet. It is overwritten by newer states.
	pendingEvent AnnounceEvent
	Key          string
	tiers        []TrackerTier

	uploadedStart   int64
	downloadedStart int64
	totalSize       int64
	mu              sync.RWMutex
	numWant         int32
	port            uint16
	debug           bool
	active          bool
	// inFlight marks that a round is currently executing. Only one round runs
	// at a time; events arriving meanwhile are latched below and executed
	// right after the current round completes.
	inFlight bool
	// pendingCompleted latches the one-shot completed event. It is sent
	// before pendingEvent and never replaced by a newer started/stopped.
	pendingCompleted bool
	// reannounce requests one manual regular round throttled by
	// EarliestAnnounce instead of NextAnnounce.
	reannounce bool
}

// New creates a Trackers instance. ctx controls the announce loop lifetime.
func New(ctx context.Context, cfg Config) *Trackers {
	return &Trackers{
		ctx:      ctx,
		log:      cfg.Log,
		Errors:   xsync.NewMap[string, string](),
		Seeds:    xsync.NewMap[string, int](),
		Leechers: xsync.NewMap[string, int](),
		Key:      cfg.Key,

		http:       cfg.HTTP,
		trackerSem: cfg.TrackerSem,
		infoHash:   cfg.InfoHash,
		peerID:     cfg.PeerID,
		port:       cfg.Port,

		uploaded:        cfg.Uploaded,
		uploadedStart:   cfg.UploadedStart,
		downloaded:      cfg.Downloaded,
		downloadedStart: cfg.DownloadedStart,
		completed:       cfg.Completed,
		totalSize:       cfg.TotalSize,
		numWant:         cfg.NumWant,

		wakeCh:  make(chan struct{}, 1),
		peersCh: cfg.PeersCh,
		debug:   cfg.Debug,
	}
}

// Run starts the announce loop and blocks until the context is cancelled.
// Callers must invoke this in a goroutine.
func (t *Trackers) Run() {
	t.loop()
}

// Start activates periodic announcing and schedules one event=started round.
// maxDelay is the stagger window of the whole round; zero announces
// immediately.
func (t *Trackers) Start(maxDelay time.Duration) {
	now := time.Now()
	window := maxDelay
	if window > 0 {
		window = min(max(window, 5*time.Second), 60*time.Minute)
	}

	t.mu.Lock()
	t.active = true
	t.pendingEvent = EventStarted
	t.pendingAt = now
	if window > 0 {
		t.pendingAt = now.Add(time.Duration(rand.Int64N(int64(window))))
	}
	t.mu.Unlock()
	t.wake()
}

// Announce submits an externally-triggered lifecycle event. A stopped event
// terminates periodic announcing; all other events keep the announce chain
// active.
func (t *Trackers) Announce(event AnnounceEvent) {
	now := time.Now()
	t.mu.Lock()
	switch event {
	case EventCompleted:
		t.pendingCompleted = true
		// A staggered started that has not been sent yet must follow the
		// completed event immediately instead of waiting out its window.
		if t.pendingEvent == EventStarted && now.Before(t.pendingAt) {
			t.pendingAt = now
		}
	case EventStopped:
		t.active = false
		t.pendingEvent = EventStopped
		t.pendingAt = now
	}
	t.mu.Unlock()
	t.wake()
}

func (t *Trackers) wake() {
	select {
	case t.wakeCh <- struct{}{}:
	default:
	}
}

// Reannounce schedules one immediate regular round. It returns false when the
// chain is inactive, a lifecycle event is pending, a round is in flight, or
// no tracker has reached its EarliestAnnounce yet.
func (t *Trackers) Reannounce() bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.inFlight || t.pendingCompleted || t.pendingEvent != "" {
		return false
	}
	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			if !now.Before(tr.EarliestAnnounce) {
				t.reannounce = true
				t.wake()
				return true
			}
		}
	}
	return false
}

// Totals returns the max seeders and leechers across all trackers.
func (t *Trackers) Totals() (seeders, leechers int) {
	t.Seeds.Range(func(_ string, s int) bool {
		if s > seeders {
			seeders = s
		}
		return true
	})
	t.Leechers.Range(func(_ string, l int) bool {
		if l > leechers {
			leechers = l
		}
		return true
	})
	return
}

// HasNoLeechers reports whether every tracker that has reported leecher
// counts says the swarm currently has zero downloaders. Returns false when no
// tracker has reported yet (or all reports are stale-missing), so callers
// keep their conservative default of assuming leechers may exist.
func (t *Trackers) HasNoLeechers() bool {
	anyReport := false
	hasLeecher := false
	t.Leechers.Range(func(_ string, l int) bool {
		anyReport = true
		if l > 0 {
			hasLeecher = true
			return false
		}
		return true
	})
	return anyReport && !hasLeecher
}

// SetError updates the error message for a tracker URL.

// Info holds tracker metadata for API responses.
type Info struct {
	URL  string
	Err  string
	Tier int
}

// SetTiers replaces the tracker tier list. It does not schedule anything by
// itself: trackers are picked up by the next round (their NextAnnounce is
// zero-valued, i.e. immediately due).
func (t *Trackers) SetTiers(tiers []TrackerTier) {
	t.mu.Lock()
	t.tiers = tiers
	t.mu.Unlock()
	t.wake()
}

// Add adds a tracker URL at the given tier. The new tracker is announced by
// the next regular round without a lifecycle event.
func (t *Trackers) Add(url string, tier int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, existingTier := range t.tiers {
		for _, tr := range existingTier.Trackers {
			if tr.URL == url {
				return
			}
		}
	}

	tr := &Tracker{URL: url}
	if t.active {
		// Mark the new tracker immediately due so the leading tier drives one
		// regular round for it without touching the other trackers.
		tr.NextAnnounce = time.Now()
	}
	if tier >= 0 && tier < len(t.tiers) {
		t.tiers[tier].Trackers = append(t.tiers[tier].Trackers, tr)
	} else {
		t.tiers = append(t.tiers, TrackerTier{Trackers: []*Tracker{tr}})
	}
	t.wake()
}

// Remove deletes a tracker by URL.
func (t *Trackers) Remove(url string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i, tier := range t.tiers {
		for j, tr := range tier.Trackers {
			if tr.URL == url {
				t.tiers[i].Trackers = slices.Delete(tier.Trackers, j, j+1)
				t.Errors.Delete(url)
				t.Seeds.Delete(url)
				t.Leechers.Delete(url)
				if len(t.tiers[i].Trackers) == 0 {
					t.tiers = slices.Delete(t.tiers, i, i+1)
				}
				t.wake()
				return
			}
		}
	}
}

// Replace renames tracker URLs. Scheduling state is kept on the same Tracker.
func (t *Trackers) Replace(replacements map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			if newURL, ok := replacements[tr.URL]; ok {
				t.Errors.Delete(tr.URL)
				if s, loaded := t.Seeds.LoadAndDelete(tr.URL); loaded {
					t.Seeds.Store(newURL, s)
				}
				if l, loaded := t.Leechers.LoadAndDelete(tr.URL); loaded {
					t.Leechers.Store(newURL, l)
				}
				tr.URL = newURL
			}
		}
	}
	t.wake()
}

// List returns all tracker info for API responses.
func (t *Trackers) List() []Info {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var infos []Info
	for i, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			errMsg, _ := t.Errors.Load(tr.URL)
			infos = append(infos, Info{Tier: i, URL: tr.URL, Err: errMsg})
		}
	}
	return infos
}

// Each calls fn for every tracker under read lock.
// The callback must not call any Trackers method that acquires the write lock
// (Add, Remove, SetTiers, Start, Announce, etc.) — this would deadlock.
func (t *Trackers) Each(fn func(tierIdx int, tr *Tracker)) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for i, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			fn(i, tr)
		}
	}
}

// URLs returns tracker URLs grouped by tier (for resume serialization).
func (t *Trackers) URLs() [][]string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	urls := make([][]string, len(t.tiers))
	for i, tier := range t.tiers {
		urls[i] = make([]string, len(tier.Trackers))
		for j, tr := range tier.Trackers {
			urls[i][j] = tr.URL
		}
	}
	return urls
}

func (t *Trackers) SetError(tr *Tracker) {
	if msg := tr.ErrorMessage(); msg != "" {
		t.Errors.Store(tr.URL, msg)
	} else {
		t.Errors.Delete(tr.URL)
	}
}

// IsActive reports whether the announce chain is currently active: periodic
// announcing is enabled and lifecycle events will be processed. Downloads use
// this to decide whether a state transition must start or stop the chain.
func (t *Trackers) IsActive() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// Shutdown synchronously sends EventStopped to every tracker that ever
// received a request, including ones with a request currently in flight.
// Transport errors are recorded on the tracker; the in-flight request may
// complete concurrently, but its response has no renewal effect because the
// chain is already inactive and the pending state has been cleared.
func (t *Trackers) Shutdown() {
	t.mu.Lock()
	t.active = false
	t.pendingEvent = ""
	t.pendingAt = time.Time{}
	t.pendingCompleted = false
	t.reannounce = false
	var trackers []*Tracker
	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			if tr.everAttempted.Load() {
				trackers = append(trackers, tr)
			}
		}
	}
	t.mu.Unlock()
	t.wake()

	for _, tr := range trackers {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := t.announceReqWithSem(ctx, EventStopped, tr.URL, 5*time.Second)
		cancel()

		if err != nil {
			t.mu.Lock()
			tr.Err = err
			t.SetError(tr)
			t.mu.Unlock()
		}
	}
}

// loop owns the only timer. It dispatches at most one round at a time and
// sleeps until the earliest NextAnnounce (or a staggered pending event).
func (t *Trackers) loop() {
	defer t.log.Debug().Msg("tracker loop: exiting")

	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		if t.runDueRound() {
			// A round was dispatched; re-check immediately for latched events.
			continue
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var timerC <-chan time.Time
		if next, ok := t.earliestNextAnnounce(); ok {
			timer.Reset(max(time.Until(next), 0))
			timerC = timer.C
		}

		select {
		case <-t.ctx.Done():
			return
		case <-t.wakeCh:
		case <-timerC:
		}
	}
}

// runDueRound dispatches the next round if any is due: a latched lifecycle
// event, a manual reannounce, or a regular round with an expired tracker.
// Returns true when a round was dispatched (in its own goroutine).
func (t *Trackers) runDueRound() bool {
	now := time.Now()
	t.mu.Lock()
	if t.inFlight {
		t.mu.Unlock()
		return false
	}

	var event AnnounceEvent
	if t.pendingCompleted {
		event = EventCompleted
		t.pendingCompleted = false
	} else if t.pendingEvent != "" && !now.Before(t.pendingAt) {
		event = t.pendingEvent
		t.pendingEvent = ""
		t.pendingAt = time.Time{}
	}
	if event != "" {
		t.inFlight = true
		t.inFlightEvent = event
		t.mu.Unlock()
		go t.runRound(event, false)
		return true
	}

	if t.reannounce {
		t.reannounce = false
		if !t.active {
			t.mu.Unlock()
			return false
		}
		t.inFlight = true
		t.mu.Unlock()
		go t.runRound("", true)
		return true
	}

	if t.active && t.anyDueLocked(now) {
		t.inFlight = true
		t.mu.Unlock()
		go t.runRound("", false)
		return true
	}

	t.mu.Unlock()
	return false
}

// anyDueLocked reports whether a regular round has work to do: some tracker in
// the first non-empty tier is due. The leading tier drives the regular rhythm;
// backup tiers are only reached through the failover path inside a round, so
// their expiry alone never starts one (otherwise a quiet-but-alive leading
// tier would spin empty rounds).
func (t *Trackers) anyDueLocked(now time.Time) bool {
	for _, tier := range t.tiers {
		if len(tier.Trackers) == 0 {
			continue
		}
		for _, tr := range tier.Trackers {
			if !tr.NextAnnounce.IsZero() && !now.Before(tr.NextAnnounce) {
				return true
			}
		}
		return false
	}
	return false
}

// earliestNextAnnounce returns the next time the loop must wake up: a
// staggered pending event or the earliest leading-tier NextAnnounce. Backup
// tier times are excluded: they are only reached through the failover path
// inside a round, so they must not wake the loop on their own.
func (t *Trackers) earliestNextAnnounce() (time.Time, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.inFlight {
		// A round is executing: the trackers it announced still hold the
		// stale NextAnnounce that started the round, so a timer set on that
		// time would fire immediately and busy-spin for the whole request.
		// finishRound always wakes the loop when the round completes, so the
		// loop only needs to sleep on the wake channel here.
		return time.Time{}, false
	}

	var next time.Time
	var ok bool
	consider := func(at time.Time) {
		if at.IsZero() {
			return
		}
		if !ok || at.Before(next) {
			next = at
			ok = true
		}
	}
	if t.pendingCompleted {
		consider(time.Now())
	} else if t.pendingEvent != "" {
		consider(t.pendingAt)
	}
	for _, tier := range t.tiers {
		if len(tier.Trackers) == 0 {
			continue
		}
		for _, tr := range tier.Trackers {
			consider(tr.NextAnnounce)
		}
		break
	}
	return next, ok
}

// runRound executes one group-level announce round in its own goroutine.
// A stopped round broadcasts stopped to every attempted tracker; any other
// round walks the tiers in order (BEP 12).
func (t *Trackers) runRound(event AnnounceEvent, useEarliest bool) {
	defer t.finishRound()

	if event == EventStopped {
		t.announceStopped()
		return
	}
	t.attemptTiers(event, useEarliest)
}

func (t *Trackers) finishRound() {
	t.mu.Lock()
	t.inFlight = false
	t.inFlightEvent = ""
	t.mu.Unlock()
	t.wake()
}

// attemptTiers walks the tiers in order. Within a tier all eligible trackers
// are announced in parallel; when at least one succeeds the round ends. Only
// when the whole tier is attempted and every attempt fails does the round
// advance to the next tier. A tracker that is still throttled (not due yet)
// ends the round: the tier is alive, just quiet, so backups must not be
// activated.
func (t *Trackers) attemptTiers(event AnnounceEvent, useEarliest bool) {
	now := time.Now()

	// Deep-copy the tier list so a concurrent Remove/Replace during the HTTP
	// requests cannot race with reading the tracker slices.
	t.mu.RLock()
	tiers := make([]TrackerTier, len(t.tiers))
	for i, tier := range t.tiers {
		tiers[i].Trackers = slices.Clone(tier.Trackers)
	}
	t.mu.RUnlock()

	for i := range tiers {
		tier := tiers[i]
		if len(tier.Trackers) == 0 {
			continue
		}

		type attempt struct {
			resp    AnnounceResponse
			skipped bool
		}
		attempts := make([]attempt, len(tier.Trackers))
		var wg sync.WaitGroup
		skippedAny := false
		for j, tr := range tier.Trackers {
			if event == "" {
				if useEarliest {
					if now.Before(tr.EarliestAnnounce) {
						attempts[j].skipped = true
						skippedAny = true
						continue
					}
				} else if !tr.NextAnnounce.IsZero() && now.Before(tr.NextAnnounce) {
					attempts[j].skipped = true
					skippedAny = true
					continue
				}
			}
			wg.Add(1)
			go func(j int, tr *Tracker) {
				defer wg.Done()
				tr.everAttempted.Store(true)
				attempts[j].resp = t.announceHTTP(tr, event)
			}(j, tr)
		}
		wg.Wait()

		var best *Tracker
		for j, tr := range tier.Trackers {
			if attempts[j].skipped {
				continue
			}
			t.applyAnnounceResult(tr, attempts[j].resp)
			if attempts[j].resp.Err == nil && best == nil {
				best = tr
			}
		}
		if best != nil {
			t.mu.Lock()
			if i < len(t.tiers) {
				t.moveToFrontLocked(i, best)
			}
			t.mu.Unlock()
			return
		}
		if skippedAny {
			// Some tracker in this tier is still within its interval: the
			// tier is alive but quiet, so do not fall through to backups.
			return
		}
	}
}

// moveToFrontLocked moves tr to the front of its tier (BEP 12: a successful
// connection is moved to the front of the tier). Caller must hold t.mu.
func (t *Trackers) moveToFrontLocked(tierIdx int, tr *Tracker) {
	tier := &t.tiers[tierIdx]
	for j, x := range tier.Trackers {
		if x == tr {
			if j > 0 {
				copy(tier.Trackers[1:j+1], tier.Trackers[:j])
				tier.Trackers[0] = tr
			}
			return
		}
	}
}

// applyAnnounceResult records one tracker's response. A tracker removed while
// its request was in flight has all side effects dropped.
func (t *Trackers) applyAnnounceResult(tr *Tracker, r AnnounceResponse) {
	now := time.Now()
	interval, minDelta := computeIntervals(r)

	t.mu.Lock()
	if !t.containsTrackerLocked(tr) {
		t.mu.Unlock()
		return
	}
	tr.LastAnnounceTime = now
	tr.EarliestAnnounce = now.Add(minDelta)
	tr.Interval = interval
	tr.FailureMessage = r.FailedReason
	tr.NextAnnounce = now.Add(interval)
	if r.Err != nil {
		tr.Err = r.Err
		t.SetError(tr)
		t.mu.Unlock()
		return
	}
	tr.Err = nil
	tr.PeerCount = len(r.Peers)
	t.SetError(tr)
	t.mu.Unlock()

	if r.Seeders > 0 {
		t.Seeds.Store(tr.URL, r.Seeders)
	}
	// Store leecher counts whenever the tracker explicitly reported an
	// "incomplete" field (including zero). Only explicit reports are stored so
	// callers can distinguish "swarm has no leechers" from "no data yet".
	if r.LeechersKnown {
		t.Leechers.Store(tr.URL, r.Leechers)
	}

	r.Peers = lo.Uniq(r.Peers)
	if len(r.Peers) > 0 && t.peersCh != nil {
		peers := make([]DiscoveredPeer, len(r.Peers))
		for i, addr := range r.Peers {
			peers[i] = DiscoveredPeer{AddrPort: addr, Source: PeerSourceTracker}
		}
		t.peersCh <- peers
	}
}

// containsTrackerLocked reports whether tr is still configured.
// Caller must hold t.mu.
func (t *Trackers) containsTrackerLocked(tr *Tracker) bool {
	for _, tier := range t.tiers {
		if slices.Contains(tier.Trackers, tr) {
			return true
		}
	}
	return false
}

// announceStopped sends stopped to every tracker that ever received a
// request. Trackers that never announced (untouched backups) have no record
// on the tracker side and are skipped.
func (t *Trackers) announceStopped() {
	t.mu.RLock()
	var trackers []*Tracker
	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			if tr.everAttempted.Load() {
				trackers = append(trackers, tr)
			}
		}
	}
	t.mu.RUnlock()

	var wg sync.WaitGroup
	for _, tr := range trackers {
		wg.Add(1)
		go func(tr *Tracker) {
			defer wg.Done()
			_, err := t.announceReqWithSem(t.ctx, EventStopped, tr.URL, 15*time.Second)
			if err != nil {
				t.mu.Lock()
				tr.Err = err
				t.SetError(tr)
				t.mu.Unlock()
			}
		}(tr)
	}
	wg.Wait()
}

// random5to10Min returns a random duration between 5 and 10 minutes at second granularity.
func random5to10Min() time.Duration {
	return time.Duration(5*60+rand.IntN(301)) * time.Second
}

// computeIntervals derives the per-tracker announce interval and min interval
// from a response:
//
//  1. Only min_interval (no interval): interval = min_interval * 2 + random(5-10min)
//  2. Only interval (no min_interval): min_interval = interval, interval jittered ±15%
//  3. Both returned: min_interval as-is, interval jittered ±15%
//  4. Neither returned: default both to 30 min
//
// The jitter (rules 2/3) and the random add-on (rules 1/4) desynchronize
// announces across downloads so a restart of many torrents does not make
// them all announce at the same instant.
func computeIntervals(r AnnounceResponse) (interval, minDelta time.Duration) {
	switch {
	case r.MinInterval > 0 && r.Interval == 0:
		minDelta = r.MinInterval
		interval = minDelta*2 + random5to10Min()
	case r.MinInterval == 0 && r.Interval > 0:
		minDelta = r.Interval
		interval = jitterInterval(r.Interval, minDelta)
	case r.MinInterval > 0 && r.Interval > 0:
		minDelta = r.MinInterval
		interval = jitterInterval(r.Interval, minDelta)
	default:
		// Neither returned — use defaults.
		minDelta = 30 * time.Minute
		interval = 30*time.Minute + random5to10Min()
	}
	return interval, minDelta
}

// jitterInterval applies a relative ±15% jitter to an announce interval so
// that downloads sharing a tracker do not announce in lockstep. The result is
// clamped to stay at or above minDelta so the tracker's min_interval is never
// violated (a negative jitter would otherwise announce too early).
func jitterInterval(interval, minDelta time.Duration) time.Duration {
	jittered := max(time.Duration(float64(interval)*(0.85+0.3*rand.Float64())), minDelta)
	return jittered
}

func (t *Trackers) announceHTTP(tr *Tracker, event AnnounceEvent) AnnounceResponse {
	resp, err := t.announceReqWithSem(t.ctx, event, tr.URL, 15*time.Second)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return AnnounceResponse{Err: errors.New("http request timeout")}
		}
		return AnnounceResponse{Err: err}
	}

	var r trackerAnnounceResponse
	body := resp.Body()
	if err := bencode.UnmarshalRelaxed(body, &r); err != nil {
		if t.debug {
			return AnnounceResponse{Err: fmt.Errorf("failed to parse tracker announce response %v: %s", err, base64.StdEncoding.EncodeToString(body))}
		}
		return AnnounceResponse{Err: errgo.Wrap(err, "failed to parse tracker announce response")}
	}

	result := AnnounceResponse{
		Interval:     time.Second * time.Duration(r.Interval),
		MinInterval:  time.Second * time.Duration(r.MinInterval),
		FailedReason: r.FailureReason,
	}
	if r.Complete != nil {
		result.Seeders = *r.Complete
	}
	if r.Incomplete != nil {
		result.Leechers = *r.Incomplete
		result.LeechersKnown = true
	}

	if len(r.Peers) != 0 {
		if r.Peers[0] == 'l' && r.Peers[len(r.Peers)-1] == 'e' {
			result.Peers = parseNonCompact(r.Peers)
		} else {
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)
			if err := bencode.Unmarshal(r.Peers, &b.B); err != nil {
				if t.debug {
					result.Err = fmt.Errorf("failed to parse binary 'peers' %v: %s", err, base64.StdEncoding.EncodeToString(r.Peers))
				} else {
					result.Err = errgo.Wrap(err, "failed to parse binary 'peers'")
				}
				return result
			}
			if b.Len()%6 != 0 {
				result.Err = fmt.Errorf("invalid binary peers length %d", b.Len())
				return result
			}
			result.Peers = make([]netip.AddrPort, 0, b.Len()/6)
			for i := 0; i < b.Len(); i += 6 {
				result.Peers = append(result.Peers, ParseCompact4(b.B[i:i+6]))
			}
		}
	}

	if len(r.Peers6) != 0 {
		if r.Peers6[0] == 'l' && r.Peers6[len(r.Peers6)-1] == 'e' {
			result.Peers = append(result.Peers, parseNonCompact(r.Peers6)...)
		} else {
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)
			if err := bencode.Unmarshal(r.Peers6, &b.B); err != nil {
				if t.debug {
					result.Err = fmt.Errorf("failed to parse binary 'peers6' %v: %s", err, base64.StdEncoding.EncodeToString(r.Peers6))
				} else {
					result.Err = errgo.Wrap(err, "failed to parse binary 'peers6'")
				}
				return result
			}
			if b.Len()%18 != 0 {
				result.Err = fmt.Errorf("invalid binary peers6 length %d", b.Len())
				return result
			}
			for i := 0; i < b.Len(); i += 18 {
				result.Peers = append(result.Peers, ParseCompact6(b.B[i:i+18]))
			}
		}
	}

	slices.SortFunc(result.Peers, func(a, b netip.AddrPort) int {
		return bytes.Compare(a.Addr().AsSlice(), b.Addr().AsSlice())
	})

	return result
}

// announceReqWithSem acquires the tracker semaphore (if configured), makes the
// HTTP GET request with the given timeout, and releases the semaphore on return.
//
// The semaphore is acquired with acquireCtx; the request timeout is derived
// from the same context only after the semaphore has been acquired. Regular
// announces pass the download's lifecycle context (t.ctx), so waiting for a
// semaphore slot never consumes the request's timeout budget: a congested
// semaphore delays the announce instead of failing it. Callers that must bound
// the total wait (e.g. Shutdown) pass a pre-timed context instead.
func (t *Trackers) announceReqWithSem(acquireCtx context.Context, event AnnounceEvent, url string, timeout time.Duration) (*resty.Response, error) {
	if t.trackerSem != nil {
		if err := t.trackerSem.Acquire(acquireCtx, 1); err != nil {
			return nil, err
		}
		defer t.trackerSem.Release(1)
	}

	reqCtx, cancel := context.WithTimeout(acquireCtx, timeout)
	defer cancel()

	return t.announceReq(reqCtx, event).Get(url)
}

func (t *Trackers) announceReq(ctx context.Context, event AnnounceEvent) *resty.Request {
	req := t.http.R().
		SetContext(ctx).
		SetQueryParam("info_hash", t.infoHash).
		SetQueryParam("peer_id", t.peerID).
		SetQueryParam("port", strconv.Itoa(int(t.port))).
		SetQueryParam("compact", "1").
		SetQueryParam("key", t.Key).
		SetQueryParam("uploaded", strconv.FormatInt(t.uploaded.Load()-t.uploadedStart, 10)).
		SetQueryParam("downloaded", strconv.FormatInt(t.downloaded.Load()-t.downloadedStart, 10)).
		SetQueryParam("left", strconv.FormatInt(t.totalSize-t.completed.Load(), 10))
	if t.numWant > 0 && event != EventStopped {
		req.SetQueryParam("numwant", strconv.Itoa(int(t.numWant)))
	}
	if event != "" {
		req.SetQueryParam("event", string(event))
	}
	return req
}

// ---- internal types and helpers ----

type trackerAnnounceResponse struct {
	Complete      *int             `bencode:"complete"`
	Incomplete    *int             `bencode:"incomplete"`
	FailureReason string           `bencode:"failure reason"`
	Peers         bencode.RawBytes `bencode:"peers"`
	Peers6        bencode.RawBytes `bencode:"peers6"`
	Interval      int64            `bencode:"interval"`
	MinInterval   int64            `bencode:"min interval"`
}

type nonCompactPeer struct {
	IP   string `bencode:"ip"`
	Port uint16 `bencode:"port"`
}

func parseNonCompact(data []byte) []netip.AddrPort {
	var s []nonCompactPeer
	if err := bencode.Unmarshal(data, &s); err != nil {
		return nil
	}
	r := make([]netip.AddrPort, 0, len(s))
	for _, item := range s {
		a, err := netip.ParseAddr(item.IP)
		if err != nil {
			continue
		}
		r = append(r, netip.AddrPortFrom(a, item.Port))
	}
	return r
}

// ParseCompact4 parses a 6-byte compact IPv4 peer address.
func ParseCompact4(b []byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), binary.BigEndian.Uint16(b[4:6]))
}

// ParseCompact6 parses an 18-byte compact IPv6 peer address.
func ParseCompact6(b []byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom16([16]byte(b[:16])), binary.BigEndian.Uint16(b[16:18]))
}
