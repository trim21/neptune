// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"
	"github.com/valyala/bytebufferpool"

	"neptune/internal/metainfo"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/global"
)

type AnnounceEvent string

const (
	EventStarted   AnnounceEvent = "started"
	EventCompleted AnnounceEvent = "Completed"
	EventStopped   AnnounceEvent = "stopped"
)

func (d *Download) setAnnounceList(trackers metainfo.AnnounceList) {
	if global.Dev {
		go func() {
			for {
				d.pendingPeersMutex.Lock()
				for i, s := range []string{
					//"192.168.1.3:50025",
					"192.168.1.3:6885",
					//"127.0.0.1:51343",
				} {
					d.pendingPeers.Push(peerWithPriority{
						addrPort: netip.MustParseAddrPort(s),
						priority: uint32(i),
					})
				}
				d.pendingPeersMutex.Unlock()
				d.pendingPeersSignal <- empty.Empty{}
				time.Sleep(60 * time.Second)
			}
		}()
	}

	for _, tier := range trackers {
		t := TrackerTier{trackers: lo.Map(lo.Shuffle(tier), func(item string, index int) *Tracker {
			return &Tracker{url: item, nextAnnounce: time.Now()}
		})}

		d.trackers = append(d.trackers, t)
	}
}

func (d *Download) backgroundTrackerHandler() {
	timer := time.NewTimer(time.Second * 10)
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
			d.TryAnnounce()
		}
	}
}

func (d *Download) TryAnnounce() {
	if d.announcePending.CompareAndSwap(false, true) {
		defer d.announcePending.Store(false)
		d.announce("")
		d.pendingPeersSignal <- empty.Empty{}
	}
}

func (d *Download) announce(event AnnounceEvent) {
	d.trackerMutex.Lock()
	defer d.trackerMutex.Unlock()

	for _, tier := range d.trackers {
		r, announced, err := tier.Announce(d, event)
		if err != nil {
			continue
		}

		if !announced {
			return
		}

		if len(r.Peers) != 0 {
			d.pendingPeersMutex.Lock()
			for _, peer := range r.Peers {
				d.pendingPeers.Push(peerWithPriority{
					addrPort: peer,
					priority: d.c.PeerPriority(peer),
				})
			}
			d.pendingPeersMutex.Unlock()
			d.pendingPeersSignal <- empty.Empty{}
		}
		return
	}
}

type TrackerTier struct {
	trackers []*Tracker
}

func (tier TrackerTier) Announce(d *Download, event AnnounceEvent) (AnnounceResult, bool, error) {
	if event == EventStopped {
		tier.announceStop(d)
		return AnnounceResult{}, false, nil
	}

	for _, t := range tier.trackers {
		now := time.Now()
		if t.nextAnnounce.After(now) {
			return AnnounceResult{}, false, nil
		}

		r := t.announce(d, event)
		if r.Interval == 0 {
			r.Interval = defaultTrackerInterval
		}

		t.lastAnnounceTime = now
		t.nextAnnounce = now.Add(r.Interval)

		if r.Err != nil {
			t.err = r.Err
			continue
		}

		t.peerCount = len(r.Peers)

		r.Peers = lo.Uniq(r.Peers)

		return r, true, nil
	}

	return AnnounceResult{}, false, nil
}

func (tier TrackerTier) announceStop(d *Download) {
	for _, t := range tier.trackers {
		if !t.lastAnnounceTime.IsZero() {
			_ = t.announceStop(d)
		}
	}
}

type nonCompactAnnounceResponse struct {
	IP   string `bencode:"ip"`
	Port uint16 `bencode:"port"`
}

func parseNonCompatResponse(data []byte) []netip.AddrPort {
	var s []nonCompactAnnounceResponse
	if err := bencode.Unmarshal(data, &s); err != nil {
		return nil
	}

	var results = make([]netip.AddrPort, 0, len(s))
	for _, item := range s {
		a, err := netip.ParseAddr(item.IP)
		if err != nil {
			continue
		}
		results = append(results, netip.AddrPortFrom(a, item.Port))
	}

	return results
}

type AnnounceResult struct {
	Err          error
	FailedReason string
	Peers        []netip.AddrPort
	Interval     time.Duration
}

type trackerAnnounceResponse struct {
	FailureReason string           `bencode:"failure reason"`
	Peers         bencode.RawBytes `bencode:"peers"`
	Peers6        bencode.RawBytes `bencode:"peers6"`
	Interval      int64            `bencode:"interval"`
	Complete      int              `bencode:"complete"`
	Incomplete    int              `bencode:"incomplete"`
}

type Tracker struct {
	lastAnnounceTime time.Time
	nextAnnounce     time.Time
	err              error
	failureMessage   string
	url              string
	peerCount        int
	//leechers         int
	//seeders          int
}

func (t *Tracker) req(d *Download) *resty.Request {
	req := d.c.http.R().
		SetQueryParam("info_hash", d.info.Hash.AsString()).
		SetQueryParam("peer_id", d.peerID.AsString()).
		SetQueryParam("port", strconv.FormatUint(uint64(d.c.Config.App.P2PPort), 10)).
		SetQueryParam("compat", "1").
		SetQueryParam("key", d.trackerKey).
		SetQueryParam("uploaded", strconv.FormatInt(d.uploaded.Load()-d.uploadAtStart, 10)).
		SetQueryParam("downloaded", strconv.FormatInt(d.downloaded.Load()-d.downloadAtStart, 10)).
		SetQueryParam("left", strconv.FormatInt(d.info.TotalLength-d.completed.Load(), 10))

	if v4 := d.c.ipv4.Load(); v4 != nil {
		req.SetQueryParam("ipv4", v4.String())
	}

	if v6 := d.c.ipv4.Load(); v6 != nil {
		req.SetQueryParam("ipv6", v6.String())
	}

	return req
}

const defaultTrackerInterval = time.Minute * 30

func (t *Tracker) announce(d *Download, event AnnounceEvent) AnnounceResult {
	d.log.Trace().Str("url", t.url).Msg("announce to tracker")

	req := t.req(d)

	if event != "" {
		req = req.SetQueryParam("event", string(event))
	}

	res, err := req.Get(t.url)
	if err != nil {
		return AnnounceResult{Err: errgo.Wrap(err, "failed to connect to tracker")}
	}

	var r trackerAnnounceResponse
	err = bencode.Unmarshal(res.Body(), &r)
	if err != nil {
		log.Debug().Err(err).Str("res", res.String()).Msg("failed to decode tracker response")
		return AnnounceResult{Err: errgo.Wrap(err, "failed to parse torrent announce response")}
	}

	var result = AnnounceResult{
		Interval:     defaultTrackerInterval,
		FailedReason: r.FailureReason,
	}

	if r.Interval != 0 {
		result.Interval = time.Second * time.Duration(r.Interval)
	}

	// BEP says we must support both format
	if len(r.Peers) != 0 {
		if r.Peers[0] == 'l' && r.Peers[len(r.Peers)-1] == 'e' {
			result.Peers = parseNonCompatResponse(r.Peers)
			// non compact response
		} else {
			// compact response
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)
			err = bencode.Unmarshal(r.Peers, &b.B)
			if err != nil {
				result.Err = errgo.Wrap(err, "failed to parse binary format 'peers'")
				return result
			}

			if b.Len()%6 != 0 {
				result.Err = fmt.Errorf("invalid binary peers6 length %d", b.Len())
				return result
			}

			result.Peers = make([]netip.AddrPort, 0, len(b.B)/6)
			for i := 0; i < len(b.B); i += 6 {
				result.Peers = append(result.Peers, parseCompact4(b.B[i:i+6]))
			}
		}

		slices.SortFunc(result.Peers, func(a, b netip.AddrPort) int {
			return bytes.Compare(a.Addr().AsSlice(), b.Addr().AsSlice())
		})
	}

	if len(r.Peers6) != 0 {
		if r.Peers6[0] == 'l' && r.Peers6[len(r.Peers6)-1] == 'e' {
			// non compact response
			result.Peers = append(result.Peers, parseNonCompatResponse(r.Peers6)...)
		} else {
			// compact response
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)

			err = bencode.Unmarshal(r.Peers6, &b.B)
			if err != nil {
				result.Err = errgo.Wrap(err, "failed to parse binary format 'peers6'")
				return result
			}

			if b.Len()%18 != 0 {
				result.Err = fmt.Errorf("invalid binary peers6 length %d", b.Len())
				return result
			}

			for i := 0; i < b.Len(); i += 18 {
				result.Peers = append(result.Peers, parseCompact6(b.B[i:i+18]))
			}
		}
	}

	result.Peers = lo.Uniq(result.Peers)

	return result
}

func (t *Tracker) announceStop(d *Download) error {
	d.log.Trace().Str("url", t.url).Msg("announce to tracker")

	_, err := t.req(d).
		SetQueryParam("event", string(EventStopped)).
		Get(t.url)
	if err != nil {
		return errgo.Wrap(err, "failed to parse torrent announce response")
	}

	return nil
}

// ScrapeUrl return enabled tracker url for scrape request
func (d *Download) ScrapeUrl() string {
	// TODO : todo
	panic("not implemented")
	//d.m.RLock()
	//defer d.m.RUnlock()

	//for _, tier := range d.trackers {
	//	for _, t := range tier.trackers {
	//}
	//}
}

type peerWithPriority struct {
	addrPort netip.AddrPort
	priority uint32
}

func (p peerWithPriority) Less(o peerWithPriority) bool {
	// reversed order, so higher priority get handled first
	return p.priority > o.priority
}

func parseCompact4(b []byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(b[:4])), binary.BigEndian.Uint16(b[4:6]))
}

func parseCompact6(b []byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom16([16]byte(b[:16])), binary.BigEndian.Uint16(b[16:18]))
}
