// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package tracker manages BitTorrent tracker announce loops.
package tracker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"
	"github.com/valyala/bytebufferpool"
	"go.uber.org/atomic"
)

type AnnounceEvent string

const (
	EventStarted   AnnounceEvent = "started"
	EventCompleted AnnounceEvent = "Completed"
	EventStopped   AnnounceEvent = "stopped"
)

// AnnounceResponse is the parsed result from a tracker announce.
type AnnounceResponse struct {
	Err          error
	FailedReason string
	Peers        []netip.AddrPort
	Interval     time.Duration
	Seeders      int
	Leechers     int
}

// Tracker is a single announce URL with its state.
type Tracker struct {
	URL              string
	LastAnnounceTime time.Time
	NextAnnounce     time.Time
	Err              error
	FailureMessage   string
	PeerCount        int
	inFlight         atomic.Bool
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
	HTTP            *resty.Client
	Uploaded        *atomic.Int64
	Downloaded      *atomic.Int64
	Completed       *atomic.Int64
	SelectedSize    *atomic.Int64
	OnPeers         func([]netip.AddrPort)
	Key             string
	InfoHash        string
	PeerID          string
	UploadedStart   int64
	DownloadedStart int64
	Port            uint16
}

// Trackers manages announce trackers and the background announce loop.
type Trackers struct {
	ctx             context.Context
	selectedSize    *atomic.Int64
	Seeds           *xsync.Map[string, int]
	Leechers        *xsync.Map[string, int]
	cancel          context.CancelFunc
	Errors          *xsync.Map[string, string]
	http            *resty.Client
	onPeers         func([]netip.AddrPort)
	queue           chan AnnounceEvent
	uploaded        *atomic.Int64
	completed       *atomic.Int64
	downloaded      *atomic.Int64
	peerID          string
	infoHash        string
	Key             string
	tiers           []TrackerTier
	wg              sync.WaitGroup
	startOnce       sync.Once
	paused          atomic.Bool
	resumeCh        chan struct{}
	downloadedStart int64
	uploadedStart   int64
	mu              sync.RWMutex
	port            uint16
}

// New creates a Trackers instance.
func New(cfg Config) *Trackers {
	return &Trackers{
		Errors:   xsync.NewMap[string, string](),
		Seeds:    xsync.NewMap[string, int](),
		Leechers: xsync.NewMap[string, int](),
		Key:      cfg.Key,

		http:     cfg.HTTP,
		infoHash: cfg.InfoHash,
		peerID:   cfg.PeerID,
		port:     cfg.Port,

		uploaded:        cfg.Uploaded,
		uploadedStart:   cfg.UploadedStart,
		downloaded:      cfg.Downloaded,
		downloadedStart: cfg.DownloadedStart,
		completed:       cfg.Completed,
		selectedSize:    cfg.SelectedSize,

		resumeCh: make(chan struct{}, 1),
		queue:    make(chan AnnounceEvent, 1),
		onPeers:  cfg.OnPeers,
	}
}

// Start begins the background announce loop. The loop runs until ctx is cancelled.
func (t *Trackers) Start(ctx context.Context) {
	t.startOnce.Do(func() {
		t.ctx, t.cancel = context.WithCancel(ctx)
		t.wg.Add(1)
		go t.loop()
	})
}

// Stop cancels the background loop and waits for it to finish.
func (t *Trackers) Stop() {
	if t.cancel != nil {
		t.cancel()
	}
	t.wg.Wait()
}

// Announce enqueues an announce event. Non-blocking.
func (t *Trackers) Announce(event AnnounceEvent) {
	select {
	case t.queue <- event:
	default:
	}
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

// SetError updates the error message for a tracker URL.

// Info holds tracker metadata for API responses.
type Info struct {
	URL  string
	Err  string
	Tier int
}

// SetTiers replaces the tracker tier list.
func (t *Trackers) SetTiers(tiers []TrackerTier) {
	t.mu.Lock()
	t.tiers = tiers
	t.mu.Unlock()
}

// Add adds a tracker URL at the given tier.
func (t *Trackers) Add(url string, tier int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, t := range t.tiers {
		for _, tr := range t.Trackers {
			if tr.URL == url {
				return
			}
		}
	}

	tr := &Tracker{URL: url, NextAnnounce: time.Now()}
	if tier >= 0 && tier < len(t.tiers) {
		t.tiers[tier].Trackers = append(t.tiers[tier].Trackers, tr)
	} else {
		t.tiers = append(t.tiers, TrackerTier{Trackers: []*Tracker{tr}})
	}
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
				return
			}
		}
	}
}

// Replace renames tracker URLs.
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
				tr.NextAnnounce = time.Now()
			}
		}
	}
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
// (Add, Remove, SetTiers, Pause, Resume, etc.) — this would deadlock.
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

// Stagger adds a random delay to all NextAnnounce times.
func (t *Trackers) Stagger() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			tr.NextAnnounce = tr.NextAnnounce.Add(time.Duration(rand.IntN(30*60)) * time.Second)
		}
	}
}

func (t *Trackers) SetError(tr *Tracker) {
	if msg := tr.ErrorMessage(); msg != "" {
		t.Errors.Store(tr.URL, msg)
	} else {
		t.Errors.Delete(tr.URL)
	}
}

// Pause stops the periodic announce loop and sends EventStopped to all trackers.
// Safe to call multiple times; only the first call takes effect.
func (t *Trackers) Pause() {
	if !t.paused.CompareAndSwap(false, true) {
		return
	}
	go t.announceToAll(EventStopped)
}

// Resume restarts the periodic announce loop, resets NextAnnounce for all
// trackers so the next tick announces immediately, and signals the loop to wake up.
// Safe to call multiple times; only the first call after a Pause takes effect.
func (t *Trackers) Resume() {
	if !t.paused.CompareAndSwap(true, false) {
		return
	}

	t.mu.Lock()
	for _, tier := range t.tiers {
		for _, tr := range tier.Trackers {
			tr.NextAnnounce = time.Now()
		}
	}
	t.mu.Unlock()

	select {
	case t.resumeCh <- struct{}{}:
	default:
	}
}

// announceToAll sends an announce event to every tracker (best-effort, 5s timeout per request).
// Stops early if context is cancelled or Trackers is resumed.
func (t *Trackers) announceToAll(event AnnounceEvent) {
	t.mu.RLock()
	trackers := make([]*Tracker, 0)
	for _, tier := range t.tiers {
		trackers = append(trackers, tier.Trackers...)
	}
	t.mu.RUnlock()

	for _, tr := range trackers {
		if t.ctx.Err() != nil || !t.paused.Load() {
			return
		}
		if !tr.inFlight.CompareAndSwap(false, true) {
			continue
		}

		ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
		_, err := t.announceReq(ctx, event).Get(tr.URL)
		cancel()

		if err != nil {
			t.mu.Lock()
			tr.Err = err
			t.SetError(tr)
			t.mu.Unlock()
		}

		tr.inFlight.Store(false)
	}
}

func (t *Trackers) loop() {
	defer t.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-t.ctx.Done():
			return
		case event := <-t.queue:
			t.doAnnounce(event)
		case <-t.resumeCh:
			if !t.paused.Load() {
				t.doAnnounce(EventStarted)
			}
		case <-ticker.C:
			if !t.paused.Load() {
				t.doAnnounce("")
			}
		}
	}
}

// doAnnounce finds all due trackers that are not currently in-flight and launches
// goroutines to perform the HTTP announce. The loop runs until no more due
// trackers remain, then returns without blocking.
func (t *Trackers) doAnnounce(event AnnounceEvent) {
	for {
		t.mu.Lock()
		var target *Tracker
		for _, tier := range t.tiers {
			for _, tr := range tier.Trackers {
				if !tr.inFlight.Load() && !tr.NextAnnounce.After(time.Now()) {
					target = tr
					tr.inFlight.Store(true)
					break
				}
			}
			if target != nil {
				break
			}
		}
		t.mu.Unlock()

		if target == nil {
			return
		}

		if event == EventStopped {
			go t.finishStop(target)
		} else {
			go t.finishAnnounce(target, event)
		}
	}
}

// finishAnnounce performs the HTTP announce for a single tracker and updates its state.
// Called from a goroutine spawned by doAnnounce.
func (t *Trackers) finishAnnounce(tr *Tracker, event AnnounceEvent) {
	defer tr.inFlight.Store(false)

	r := t.announceHTTP(tr, event)

	now := time.Now()
	if r.Interval == 0 {
		r.Interval = 30 * time.Minute
	}

	t.mu.Lock()
	tr.LastAnnounceTime = now
	tr.NextAnnounce = now.Add(r.Interval)
	tr.FailureMessage = r.FailedReason
	if r.Err != nil {
		tr.Err = r.Err
	} else {
		tr.Err = nil
		tr.PeerCount = len(r.Peers)
	}
	t.SetError(tr)
	t.mu.Unlock()

	if r.Err != nil {
		return
	}

	if r.Seeders > 0 {
		t.Seeds.Store(tr.URL, r.Seeders)
	}
	if r.Leechers > 0 {
		t.Leechers.Store(tr.URL, r.Leechers)
	}

	r.Peers = lo.Uniq(r.Peers)
	if len(r.Peers) > 0 && t.onPeers != nil {
		t.onPeers(r.Peers)
	}
}

// finishStop completes the stopped announce for a single tracker.
func (t *Trackers) finishStop(tr *Tracker) {
	defer tr.inFlight.Store(false)
	t.announceStop(tr)
}

func (t *Trackers) announceStop(tr *Tracker) {
	ctx, cancel := context.WithTimeout(t.ctx, 15*time.Second)
	defer cancel()

	_, err := t.announceReq(ctx, EventStopped).Get(tr.URL)
	if err != nil {
		t.mu.Lock()
		tr.Err = err
		t.SetError(tr)
		t.mu.Unlock()
	}
}

func (t *Trackers) announceHTTP(tr *Tracker, event AnnounceEvent) AnnounceResponse {
	ctx, cancel := context.WithTimeout(t.ctx, 15*time.Second)
	defer cancel()

	resp, err := t.announceReq(ctx, event).Get(tr.URL)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return AnnounceResponse{Err: errors.New("http request timeout")}
		}
		return AnnounceResponse{Err: err}
	}

	var r trackerAnnounceResponse
	if err := bencode.Unmarshal(resp.Body(), &r); err != nil {
		return AnnounceResponse{Err: errgo.Wrap(err, "failed to parse tracker announce response")}
	}

	result := AnnounceResponse{
		Interval:     30 * time.Minute,
		FailedReason: r.FailureReason,
		Seeders:      r.Complete,
		Leechers:     r.Incomplete,
	}

	if r.Interval != 0 {
		result.Interval = time.Second * time.Duration(r.Interval)
	}

	if len(r.Peers) != 0 {
		if r.Peers[0] == 'l' && r.Peers[len(r.Peers)-1] == 'e' {
			result.Peers = parseNonCompact(r.Peers)
		} else {
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)
			if err := bencode.Unmarshal(r.Peers, &b.B); err != nil {
				result.Err = errgo.Wrap(err, "failed to parse binary 'peers'")
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
				result.Err = errgo.Wrap(err, "failed to parse binary 'peers6'")
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
		SetQueryParam("left", strconv.FormatInt(t.selectedSize.Load()-t.completed.Load(), 10))
	if event != "" {
		req.SetQueryParam("event", string(event))
	}
	return req
}

// ---- internal types and helpers ----

type trackerAnnounceResponse struct {
	FailureReason string           `bencode:"failure reason"`
	Peers         bencode.RawBytes `bencode:"peers"`
	Peers6        bencode.RawBytes `bencode:"peers6"`
	Interval      int64            `bencode:"interval"`
	Complete      int              `bencode:"complete"`
	Incomplete    int              `bencode:"incomplete"`
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

// AnnounceToScrape converts an announce URL to a scrape URL per BEP 48.
func AnnounceToScrape(announceURL string) (string, bool) {
	u, err := url.Parse(announceURL)
	if err != nil {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	lastSlash := strings.LastIndex(u.Path, "/")
	lastPart := u.Path[lastSlash+1:]
	if !strings.HasPrefix(lastPart, "announce") {
		return "", false
	}
	u.Path = u.Path[:lastSlash+1] + "scrape" + lastPart[len("announce"):]
	return u.String(), true
}
