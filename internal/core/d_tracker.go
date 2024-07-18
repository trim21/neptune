// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"github.com/samber/lo"
	"github.com/trim21/errgo"
	"github.com/valyala/bytebufferpool"

	"tyr/internal/metainfo"
	"tyr/internal/pkg/global"
	"tyr/internal/pkg/null"
)

type AnnounceEvent string

const (
	EventStarted   AnnounceEvent = "started"
	EventCompleted AnnounceEvent = "completed"
	EventStopped   AnnounceEvent = "stopped"
)

func (d *Download) setAnnounceList(trackers metainfo.AnnounceList) {
	if global.Dev {
		go func() {
			for {
				d.peersMutex.Lock()
				for i, s := range []string{"192.168.1.3:50025", "192.168.1.3:6885", "127.0.0.1:51343"} {
					d.peers.Push(peerWithPriority{
						addrPort: netip.MustParseAddrPort(s),
						priority: uint32(i),
					})
				}
				d.peersMutex.Unlock()
				time.Sleep(5 * time.Minute)
			}
		}()
		return
	}

	for _, tier := range trackers {
		t := TrackerTier{trackers: lo.Map(lo.Shuffle(tier), func(item string, index int) *Tracker {
			return &Tracker{url: item, nextAnnounce: time.Now()}
		})}

		d.trackers = append(d.trackers, t)
	}
}

func (d *Download) TryAnnounce() {
	if d.announcePending.CompareAndSwap(false, true) {
		defer d.announcePending.Store(true)
		d.announce("")
		return
	}
}

func (d *Download) announce(event AnnounceEvent) {
	d.asyncAnnounce(event)
}

func (d *Download) asyncAnnounce(event AnnounceEvent) {
	// TODO: do all level tracker announce by config
	for _, tier := range d.trackers {
		r, err := tier.Announce(d, event)
		if err != nil {
			continue
		}
		if len(r.Peers) != 0 {
			d.peersMutex.Lock()
			for _, peer := range r.Peers {
				d.peers.Push(peerWithPriority{
					addrPort: peer,
					priority: d.c.PeerPriority(peer),
				})
			}
			d.peersMutex.Unlock()
		}
		return
	}
}

type TrackerTier struct {
	trackers []*Tracker
}

func (tier TrackerTier) Announce(d *Download, event AnnounceEvent) (AnnounceResult, error) {
	if event == EventStarted {
		tier.announceStop(d)
		return AnnounceResult{}, nil
	}

	for _, t := range tier.trackers {
		if !time.Now().After(t.nextAnnounce) {
			return AnnounceResult{}, nil
		}

		r, err := t.announce(d, event)
		if err != nil {
			t.m.Lock()
			t.err = err
			t.nextAnnounce = time.Now().Add(time.Minute * 30)
			t.m.Unlock()
			continue
		}

		if r.FailedReason.Set {
			t.m.Lock()
			t.err = errors.New(r.FailedReason.Value)
			t.m.Unlock()
			return AnnounceResult{}, nil
		}
		t.m.Lock()
		t.peerCount = len(r.Peers)
		t.m.Unlock()

		r.Peers = lo.Uniq(r.Peers)

		return r, nil
	}

	return AnnounceResult{}, nil
}

func (tier TrackerTier) announceStop(d *Download) {
	for _, t := range tier.trackers {
		_ = t.announceStop(d)
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
	FailedReason null.String
	Peers        []netip.AddrPort
	Interval     time.Duration
}

type trackerAnnounceResponse struct {
	FailureReason null.Null[string]        `bencode:"failure reason"`
	Peers         null.Null[bencode.Bytes] `bencode:"peers"`
	Peers6        null.Null[bencode.Bytes] `bencode:"peers6"`
	Interval      null.Null[int64]         `bencode:"interval"`
	Complete      null.Null[int]           `bencode:"complete"`
	Incomplete    null.Null[int]           `bencode:"incomplete"`
}

type Tracker struct {
	lastAnnounceTime time.Time
	nextAnnounce     time.Time
	err              error
	url              string
	peerCount        int
	//leechers         int
	//seeders          int
	m sync.RWMutex
}

func (t *Tracker) req(d *Download) *resty.Request {
	req := d.c.http.R().
		SetQueryParam("info_hash", d.info.Hash.AsString()).
		SetQueryParam("peer_id", d.peerID.AsString()).
		SetQueryParam("port", strconv.FormatUint(uint64(d.c.Config.App.P2PPort), 10)).
		SetQueryParam("compat", "1").
		SetQueryParam("uploaded", strconv.FormatInt(d.uploaded.Load()-d.uploadAtStart, 10)).
		SetQueryParam("downloaded", strconv.FormatInt(d.downloaded.Load()-d.downloadAtStart, 10)).
		SetQueryParam("left", strconv.FormatInt(d.info.TotalLength-d.completed(), 10))

	if v4 := d.c.ipv4.Load(); v4 != nil {
		req.SetQueryParam("ipv4", v4.String())
	}

	if v6 := d.c.ipv4.Load(); v6 != nil {
		req.SetQueryParam("ipv6", v6.String())
	}

	return req
}

func (t *Tracker) announce(d *Download, event AnnounceEvent) (AnnounceResult, error) {
	d.log.Trace().Str("url", t.url).Msg("announce to tracker")
	now := time.Now()

	req := t.req(d)
	t.lastAnnounceTime = now

	if event != "" {
		req = req.SetQueryParam("event", string(event))
	}

	res, err := req.Get(t.url)
	if err != nil {
		return AnnounceResult{}, errgo.Wrap(err, "failed to connect to tracker")
	}

	var r trackerAnnounceResponse
	err = bencode.Unmarshal(res.Body(), &r)
	if err != nil {
		log.Debug().Err(err).Str("res", res.String()).Msg("failed to decode tracker response")
		return AnnounceResult{}, errgo.Wrap(err, "failed to parse torrent announce response")
	}

	var m map[string]any
	fmt.Println(bencode.Unmarshal(res.Body(), &m))

	if r.FailureReason.Set {
		return AnnounceResult{FailedReason: r.FailureReason}, nil
	}

	var result = AnnounceResult{
		Interval: time.Minute * 30,
	}

	if r.Interval.Set {
		result.Interval = time.Second * time.Duration(r.Interval.Value)
	}

	t.nextAnnounce = now.Add(result.Interval)
	d.log.Trace().Str("url", t.url).Time("next", t.nextAnnounce).Msg("next announce")

	// BEP says we must support both format
	if r.Peers.Set {
		if r.Peers.Value[0] == 'l' && r.Peers.Value[len(r.Peers.Value)-1] == 'e' {
			result.Peers = parseNonCompatResponse(r.Peers.Value)
			// non compact response
		} else {
			// compact response
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)
			err = bencode.Unmarshal(r.Peers.Value, &b.B)
			if err != nil {
				return result, errgo.Wrap(err, "failed to parse binary format 'peers'")
			}

			if b.Len()%6 != 0 {
				return result, fmt.Errorf("invalid binary peers6 length %d", b.Len())
			}

			result.Peers = make([]netip.AddrPort, 0, len(b.B)/6)
			for i := 0; i < len(b.B); i += 6 {
				addr := netip.AddrFrom4([4]byte(b.B[i : i+4]))
				port := binary.BigEndian.Uint16(b.B[i+4:])
				result.Peers = append(result.Peers, netip.AddrPortFrom(addr, port))
			}
		}

		slices.SortFunc(result.Peers, func(a, b netip.AddrPort) int {
			return bytes.Compare(a.Addr().AsSlice(), b.Addr().AsSlice())
		})
	}

	if r.Peers6.Set {
		if r.Peers6.Value[0] == 'l' && r.Peers6.Value[len(r.Peers6.Value)-1] == 'e' {
			// non compact response
			result.Peers = append(result.Peers, parseNonCompatResponse(r.Peers6.Value)...)
		} else {
			// compact response
			var b = bytebufferpool.Get()
			defer bytebufferpool.Put(b)

			err = bencode.Unmarshal(r.Peers6.Value, &b.B)
			if err != nil {
				return result, errgo.Wrap(err, "failed to parse binary format 'peers6'")
			}

			if b.Len()%18 != 0 {
				return result, fmt.Errorf("invalid binary peers6 length %d", b.Len())
			}

			for i := 0; i < b.Len(); i += 18 {
				addr := netip.AddrFrom16([16]byte(b.B[i : i+16]))
				port := binary.BigEndian.Uint16(b.B[i+16:])
				result.Peers = append(result.Peers, netip.AddrPortFrom(addr, port))
			}
		}
	}

	result.Peers = lo.Uniq(result.Peers)

	return result, nil
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
