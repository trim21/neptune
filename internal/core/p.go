// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"github.com/docker/go-units"
	"github.com/kelindar/bitmap"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/pkg/global"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/null"
	"neptune/internal/pkg/random"
	"neptune/internal/proto"
	"neptune/internal/util"
	"neptune/internal/version"
)

const ourPexExtID proto.ExtensionMessage = 22

const peerIDPrefix = "-NE" +
	string(version.MAJOR+'0') +
	string(version.MINOR+'0') +
	string(version.PATCH+'0') + "0-"

func NewPeerID() (peerID proto.PeerID) {
	copy(peerID[:], peerIDPrefix)
	copy(peerID[8:], random.PrintableBytes(12))
	return
}

func NewOutgoingPeer(conn net.Conn, d *Download, addr netip.AddrPort) *Peer {
	return newPeer(conn, d, addr, false, nil)
}

func NewIncomingPeer(conn net.Conn, d *Download, addr netip.AddrPort, h proto.Handshake) *Peer {
	return newPeer(conn, d, addr, true, &h)
}

func newPeer(
	conn net.Conn,
	d *Download,
	addr netip.AddrPort,
	skipReadHandshake bool,
	h *proto.Handshake,
) *Peer {
	ctx, cancel := context.WithCancel(d.ctx)
	l := d.log.With().Stringer("addr", addr)
	var ua string
	if h != nil {
		ua = util.ParsePeerID(h.PeerID)
		l = l.Str("peer_id", url.QueryEscape(h.PeerID.AsString()))
	}

	p := &Peer{
		ctx:    ctx,
		cancel: cancel,

		log:        l.Logger(),
		Conn:       conn,
		d:          d,
		Bitmap:     bm.New(d.info.NumPieces),
		ioOut:      flowrate.New(time.Second, time.Second),
		ioIn:       flowrate.New(time.Second, time.Second),
		Address:    addr,
		QueueLimit: *atomic.NewUint32(200),
		Incoming:   skipReadHandshake,

		ourChoking:     *atomic.NewBool(true),
		ourInterested:  *atomic.NewBool(false),
		peerChoking:    *atomic.NewBool(true),
		peerInterested: *atomic.NewBool(false),

		ourPieceRequests: make(chan uint32, 1),

		UserAgent: *atomic.NewPointer[string](&ua),

		Requested: make(bitmap.Bitmap, d.bitfieldSize),

		responseCond: gsync.NewCond(gsync.EmptyLock{}),

		//ResChan:   make(chan req.Response, 1),
		myRequests:       xsync.NewMapOf[proto.ChunkRequest, time.Time](),
		myRequestHistory: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		Rejected: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		peerRequests: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		r: bufio.NewReaderSize(conn, units.KiB*18),
		w: bufio.NewWriterSize(conn, units.KiB*8),

		allowFast: bm.New(d.info.NumPieces),
		//Requested: bm.New(d.info.NumPieces),

		peerID: *atomic.NewPointer(&proto.PeerID{}),

		rttAverage: sizedSlice[time.Duration]{limit: 2000},
		lastSend:   *atomic.NewTime(time.Now()),
	}

	if h != nil {
		p.dhtEnabled = h.DhtEnabled
		p.subExtensions = h.ExchangeExtensions
		p.fastExtension = h.FastExtension
	}

	go p.start(skipReadHandshake)
	return p
}

var ErrPeerSendInvalidData = errors.New("addrPort send invalid data")

type Peer struct {
	log      zerolog.Logger
	ctx      context.Context
	Conn     net.Conn
	lastSend atomic.Time
	r        *bufio.Reader
	w        *bufio.Writer
	d        *Download
	cancel   context.CancelFunc
	Bitmap   *bm.Bitmap

	myRequests       *xsync.MapOf[proto.ChunkRequest, time.Time]
	myRequestHistory *xsync.MapOf[proto.ChunkRequest, empty.Empty]

	peerRequests *xsync.MapOf[proto.ChunkRequest, empty.Empty]

	Rejected  *xsync.MapOf[proto.ChunkRequest, empty.Empty]
	allowFast *bm.Bitmap
	ioOut     *flowrate.Monitor
	ioIn      *flowrate.Monitor
	UserAgent atomic.Pointer[string]

	ourPieceRequests chan uint32 // our requests for peer chan

	responseCond *gsync.Cond
	peerID       atomic.Pointer[proto.PeerID]
	Address      netip.AddrPort

	Requested bitmap.Bitmap

	rttAverage sizedSlice[time.Duration]

	peerChoking    atomic.Bool // peer is choking us
	peerInterested atomic.Bool
	ourChoking     atomic.Bool
	ourInterested  atomic.Bool
	QueueLimit     atomic.Uint32
	closed         atomic.Bool

	rttMutex sync.RWMutex

	wm sync.Mutex

	extDontHaveID gsync.AtomicUint[proto.ExtensionMessage]
	extPexID      gsync.AtomicUint[proto.ExtensionMessage]

	writeBuf [4]byte // buffer for reading message size and event id
	readBuf  [4]byte // buffer for reading message size and event id
	Incoming bool

	fastExtension bool
	dhtEnabled    bool
	subExtensions bool
}

func (p *Peer) Response(res *proto.ChunkResponse) {
	_, ok := p.peerRequests.LoadAndDelete(res.Request())
	if !ok {
		panic("send response without request")
	}
	p.ioOut.Update(len(res.Data))
	p.sendEventX(Event{
		Event: proto.Piece,
		Res:   res,
	})
}

func (p *Peer) Request(req proto.ChunkRequest) {
	_, exist := p.myRequests.LoadOrStore(req, time.Now())
	if exist {
		p.log.Trace().Msg("myRequests already sent")
		return
	}

	p.sendEventX(Event{
		Event: proto.Request,
		Req:   req,
	})
}

func (p *Peer) Have(index uint32) {
	p.sendEventX(Event{
		Index: index,
		Event: proto.Have,
	})
}

func (p *Peer) Unchoke() {
	p.sendEventX(Event{Event: proto.Unchoke})
}

func (p *Peer) close() {
	if p.closed.CompareAndSwap(false, true) {
		p.log.Trace().Caller(1).Msg("close")
		p.d.peers.Delete(p.Address)
		p.d.c.sem.Release(1)
		p.d.c.connectionCount.Sub(1)
		p.cancel()
		_ = p.Conn.Close()
		p.d.buildNetworkPieces <- empty.Empty{}
	}
}

func (p *Peer) ourRequestHandle() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case index := <-p.ourPieceRequests:
			chunkLen := int(pieceChunksCount(p.d.info, index))
			for i := range chunkLen {
				if p.closed.Load() {
					return
				}

				// 10 is a magic number to avoid peer reject our requests
				for p.myRequests.Size() >= min(int(p.QueueLimit.Load())-10, 300) {
					select {
					case <-p.ctx.Done():
						return
					case <-p.responseCond.C:
					}
				}

				if !p.peerChoking.Load() {
					p.Request(pieceChunk(p.d.info, index, i))
				}
			}

			p.d.scheduleRequestSignal <- empty.Empty{}
		}
	}
}

func (p *Peer) start(skipHandshake bool) {
	p.log.Trace().Msg("start")
	defer p.close()

	_ = p.Conn.SetWriteDeadline(time.Now().Add(time.Second * 30))
	if err := proto.SendHandshake(p.Conn, p.d.info.Hash, NewPeerID(), p.d.private); err != nil {
		p.log.Trace().Err(err).Msg("failed to send handshake to addrPort")
		return
	}

	if !skipHandshake {
		_ = p.Conn.SetReadDeadline(time.Now().Add(time.Second * 30))
		h, err := proto.ReadHandshake(p.r)
		if err != nil {
			p.log.Trace().Err(err).Msg("failed to read handshake")
			return
		}

		if h.InfoHash != p.d.info.Hash {
			p.log.Trace().Msgf("addrPort info hash mismatch %x", h.InfoHash)
			return
		}

		p.dhtEnabled = h.DhtEnabled
		p.fastExtension = h.FastExtension
		p.subExtensions = h.ExchangeExtensions

		p.peerID.Store(&h.PeerID)

		p.log = p.log.With().Str("peer_id", url.QueryEscape(string(h.PeerID[:]))).Logger()
		p.log.Trace().Msg("connect to addrPort")
		ua := util.ParsePeerID(h.PeerID)
		p.UserAgent.Store(&ua)
	}

	if p.fastExtension {
		p.log.Trace().Msg("support fast extension")
	}
	if p.subExtensions {
		p.log.Trace().Msg("support sub extension")
	}
	if p.dhtEnabled {
		p.log.Trace().Msg("support DHT")
	}

	// sync point, after both side send handshake and starting send peer messages

	go func() {
		p.sendInitPayload()

		if p.closed.Load() {
			return
		}

		timer := time.NewTicker(time.Second * 30)
		defer timer.Stop()

		defer p.close()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-timer.C:
				if time.Since(p.lastSend.Load()) >= time.Minute {
					p.sendEventX(Event{keepAlive: true})
				}
			}
		}
	}()

	// make it visible to download
	_, loaded := p.d.peers.LoadOrStore(p.Address, p)
	if loaded {
		// connected peers, just ignore
		return
	}

	go p.ourRequestHandle()

	for {
		if p.ctx.Err() != nil {
			return
		}

		event, err := p.DecodeEvents()
		if err != nil {
			p.log.Trace().Err(err).Msg("failed to decode event")
			return
		}

		if event.Ignored {
			continue
		}

		p.log.Trace().Stringer("event", event.Event).Msg("receive event")

		switch event.Event {
		case proto.Bitfield:
			p.Bitmap.OR(event.Bitmap)
		case proto.Have:
			if event.Index >= p.d.info.NumPieces {
				p.log.Debug().Uint32("index", event.Index).Msg("peer send 'Have' message with invalid index")
				return
			}

			p.Bitmap.Set(event.Index)
		case proto.Interested:
			p.peerInterested.Store(true)
			p.d.scheduleResponseSignal <- empty.Empty{}
		case proto.NotInterested:
			p.peerInterested.Store(false)
		case proto.Choke:
			p.peerChoking.Store(true)
		case proto.Unchoke:
			p.peerChoking.Store(false)
			p.d.scheduleRequestSignal <- empty.Empty{}
		case proto.Piece:
			if !p.resIsValid(event.Res) {
				p.log.Trace().Msg("failed to validate response")
				// send response without myRequests
				return
			}

			p.responseCond.Signal()
			p.ioIn.Update(len(event.Res.Data))
			p.d.ResChan <- event.Res
		case proto.Request:
			if !p.validateRequest(event.Req) {
				if p.fastExtension {
					go p.sendEventX(Event{Req: event.Req, Event: proto.Reject})
				}
			}

			p.peerRequests.Store(event.Req, empty.Empty{})
			p.d.scheduleResponseSignal <- empty.Empty{}
		case proto.Extended:
			if event.ExtensionID == proto.ExtensionHandshake {
				p.log.Trace().Any("ext", event.ExtHandshake).Msg("receive extension handshake")

				if event.ExtHandshake.V.Set {
					p.UserAgent.Store(&event.ExtHandshake.V.Value)
				}
				if event.ExtHandshake.QueueLength.Set {
					p.QueueLimit.Store(event.ExtHandshake.QueueLength.Value)
				}

				if event.ExtHandshake.Mapping.DontHave.Set {
					p.extDontHaveID.Store(event.ExtHandshake.Mapping.DontHave.Value)
				}

				if !p.d.private {
					if event.ExtHandshake.Mapping.Pex.Set {
						p.extPexID.Store(event.ExtHandshake.Mapping.Pex.Value)
					}
				}

				continue
			}

			if event.ExtensionID == ourPexExtID {
				added, dropped, err := parsePex(event.ExtPex)
				if err != nil {
					return
				}
				p.d.pexAdd <- added
				p.d.pexDrop <- dropped
				continue
			}

			if event.ExtensionID == p.extDontHaveID.Load() {
				p.Bitmap.Unset(event.Index)
				continue
			}

			continue
		case proto.HaveAll:
			p.Bitmap.Fill()
		case proto.HaveNone:
			p.Bitmap.Clear()
		case proto.Cancel:
			p.peerRequests.Delete(event.Req)
		case proto.Reject:
			p.log.Trace().Msgf("reject %+v", event.Req)
			p.Rejected.Store(event.Req, empty.Empty{})
			p.myRequests.Delete(event.Req)
		case proto.AllowedFast:
			if event.Index >= p.d.info.NumPieces {
				p.log.Debug().Uint32("index", event.Index).Msg("peer send 'AllowedFast' message with invalid index")
				return
			}

			p.allowFast.Set(event.Index)
		case proto.Port:
			if p.d.private { // client should not enable dht on private torrent
				return
			}
			p.d.c.dht.AddPeer(netip.AddrPortFrom(p.Address.Addr(), event.Port))
		case proto.Suggest:
		// currently ignored and unsupported
		case proto.BitCometExtension:
		}

		switch event.Event {
		case proto.Have, proto.HaveAll, proto.Bitfield:
			select {
			case p.d.scheduleRequestSignal <- empty.Empty{}:
			default:
			}

			if p.Bitmap.WithAndNot(p.d.bm).Count() != 0 {
				if p.ourInterested.CompareAndSwap(false, true) {
					go p.sendEventX(Event{Event: proto.Interested})
				}
			}
		}
	}
}

func (p *Peer) sendInitPayload() {
	bitmapCount := p.d.bm.Count()

	var err error
	switch {
	case p.fastExtension && bitmapCount == 0:
		err = p.sendEvent(Event{Event: proto.HaveNone})
	case p.fastExtension && bitmapCount == p.d.info.NumPieces:
		err = p.sendEvent(Event{Event: proto.HaveAll})
	case bitmapCount != 0:
		err = p.sendEvent(Event{Event: proto.Bitfield, Bitmap: p.d.bm})
	}

	if err != nil {
		p.close()
		return
	}

	if p.subExtensions {
		p.sendEventX(Event{
			Event:       proto.Extended,
			ExtensionID: proto.ExtensionHandshake,
			ExtHandshake: proto.ExtHandshake{
				V: null.NewString(global.UserAgent),
				Mapping: proto.ExtMapping{
					Pex: null.Null[proto.ExtensionMessage]{Value: ourPexExtID, Set: !p.d.info.Private},
				},
				QueueLength: null.NewUint32(500),
			},
		})
	}
}

func (p *Peer) sendEventX(e Event) {
	if p.sendEvent(e) != nil {
		p.close()
	}
}

func (p *Peer) sendEvent(e Event) error {
	p.wm.Lock()
	defer p.wm.Unlock()

	err := p.write(e)
	if err != nil {
		return err
	}

	if e.Event == proto.Have || e.Event == proto.Reject {
		return nil
	}

	return p.w.Flush()
}

func (p *Peer) validateRequest(req proto.ChunkRequest) bool {
	if req.PieceIndex >= p.d.info.NumPieces {
		return false
	}

	if req.Length%defaultBlockSize != 0 {
		return false
	}

	// allow 16kib and 32 kib piece
	if req.Length/defaultBlockSize > 2 {
		return false
	}

	pieceSize := as.Uint32(p.d.pieceLength(req.PieceIndex))

	return req.Begin+req.Length <= pieceSize
}

func (p *Peer) resIsValid(res *proto.ChunkResponse) bool {
	r := proto.ChunkRequest{
		PieceIndex: res.PieceIndex,
		Begin:      res.Begin,
		Length:     as.Uint32(len(res.Data)),
	}

	reqTime, ok := p.myRequests.LoadAndDelete(r)

	dur := time.Since(reqTime)

	p.rttMutex.Lock()

	p.rttAverage.Push(dur)

	p.rttMutex.Unlock()

	return ok
}

type pexPeer struct {
	addrPort         netip.AddrPort
	preferEnc        bool
	seedOnly         bool
	supportUTP       bool
	supportHolePunch bool
	outGoing         bool
}

func parsePex(msg proto.ExtPex) ([]pexPeer, []netip.AddrPort, error) {
	if len(msg.Added)%6 != 0 {
		return nil, nil, fmt.Errorf("invalid length of 'added': %d %d", len(msg.Added), len(msg.Added)%6)
	}

	if len(msg.Added)/6 != len(msg.AddedFlag) {
		return nil, nil, fmt.Errorf("added and added.f size mismatch, len(added) = %d bug len(added.f) = %d", len(msg.Added), len(msg.AddedFlag))
	}

	if len(msg.Dropped)%6 != 0 {
		return nil, nil, errors.New("invalid dropped address")
	}

	if len(msg.Added6)%18 != 0 {
		return nil, nil, fmt.Errorf("invalid length of 'added6': %d %d", len(msg.Added6), len(msg.Added6)%18)
	}

	if len(msg.Added6)/18 != len(msg.Added6Flag) {
		return nil, nil, fmt.Errorf("added6 and added6.f size mismatch, len(added6) = %d bug len(added6.f) = %d", len(msg.Added6), len(msg.Added6Flag))
	}

	if len(msg.Dropped6)%18 != 0 {
		return nil, nil, errors.New("invalid dropped6 address")
	}

	var r = make([]pexPeer, 0, len(msg.AddedFlag)+len(msg.Added6Flag))

	for i, flag := range msg.AddedFlag {
		r = append(r, pexPeer{
			addrPort:         parseCompact4(msg.Added[i*6 : i*6+6]),
			preferEnc:        flag&proto.PexFlagPreferEnc != 0,
			seedOnly:         flag&proto.PexFlagSeedOnly != 0,
			supportUTP:       flag&proto.PexFlagSupportUTP != 0,
			supportHolePunch: flag&proto.PexFlagSupportHolePunchP != 0,
			outGoing:         flag&proto.PexFlagOutgoing != 0,
		})
	}

	for i, flag := range msg.Added6Flag {
		r = append(r, pexPeer{
			addrPort:         parseCompact6(msg.Added6[i*18 : i*18+18]),
			preferEnc:        flag&proto.PexFlagPreferEnc != 0,
			seedOnly:         flag&proto.PexFlagSeedOnly != 0,
			supportUTP:       flag&proto.PexFlagSupportUTP != 0,
			supportHolePunch: flag&proto.PexFlagSupportHolePunchP != 0,
			outGoing:         flag&proto.PexFlagOutgoing != 0,
		})
	}

	var dropped = make([]netip.AddrPort, 0, len(msg.Dropped)/6+len(msg.Dropped6)/18)

	for i := 0; i < len(msg.Dropped); i += 6 {
		dropped = append(dropped, parseCompact4(msg.Dropped[i:i+6]))
	}

	for i := 0; i < len(msg.Dropped6); i += 18 {
		dropped = append(dropped, parseCompact6(msg.Dropped6[i:i+18]))
	}

	return r, dropped, nil
}

type sizedSlice[T interface{ ~int | ~int64 }] struct {
	s     []T
	limit int
	count int
}

func (t *sizedSlice[T]) Push(item T) {
	if len(t.s) < t.limit {
		t.s = append(t.s, item)
		t.count++
		return
	}

	t.s[t.count%t.limit] = item
	t.count++
}

func (t *sizedSlice[T]) Average() T {
	if len(t.s) == 0 {
		return 0
	}

	var total T
	for _, item := range t.s {
		total += item
	}

	return total / T(len(t.s))
}
