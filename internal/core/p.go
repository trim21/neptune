// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"github.com/dchest/uniuri"
	"github.com/docker/go-units"
	"github.com/fatih/color"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"tyr/internal/pkg/as"
	"tyr/internal/pkg/bm"
	"tyr/internal/pkg/empty"
	"tyr/internal/pkg/flowrate"
	"tyr/internal/pkg/gsync"
	"tyr/internal/pkg/null"
	"tyr/internal/pkg/unsafe"
	"tyr/internal/proto"
	"tyr/internal/util"
	"tyr/internal/version"
)

type PeerID [20]byte

func (i PeerID) AsString() string {
	return unsafe.Str(i[:])
}

var emptyPeerID PeerID

func (i PeerID) Zero() bool {
	return i == emptyPeerID
}

var peerIDChars = []byte("0123456789abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~")

var peerIDPrefix = fmt.Sprintf("-TY%x%x%x0-", version.MAJOR, version.MINOR, version.PATCH)

var handshakeAgent = fmt.Sprintf("Tyr %d.%d.%d", version.MAJOR, version.MINOR, version.PATCH)

func NewPeerID() (peerID PeerID) {
	copy(peerID[:], peerIDPrefix)
	copy(peerID[8:], uniuri.NewLenCharsBytes(12, peerIDChars))
	return
}

func NewOutgoingPeer(conn net.Conn, d *Download, addr netip.AddrPort) *Peer {
	return newPeer(conn, d, addr, emptyPeerID, false, false)
}

func NewIncomingPeer(conn net.Conn, d *Download, addr netip.AddrPort, h proto.Handshake) *Peer {
	return newPeer(conn, d, addr, h.PeerID, true, h.FastExtension)
}

func newPeer(
	conn net.Conn,
	d *Download,
	addr netip.AddrPort,
	peerID PeerID,
	skipHandshake bool,
	fast bool,
) *Peer {
	ctx, cancel := context.WithCancel(context.Background())
	l := d.log.With().Stringer("addr", addr)
	var ua string
	if !peerID.Zero() {
		ua = util.ParsePeerId(peerID)
		l = l.Str("peer_id", url.QueryEscape(peerID.AsString()))
	}

	p := &Peer{
		ctx:                  ctx,
		log:                  l.Logger(),
		supportFastExtension: fast,
		Conn:                 conn,
		d:                    d,
		Bitmap:               bm.New(d.info.NumPieces),
		ioOut:                flowrate.New(time.Second, time.Second),
		ioIn:                 flowrate.New(time.Second, time.Second),
		Address:              addr,
		QueueLimit:           *atomic.NewUint32(100),
		Incoming:             skipHandshake,
		amChoking:            *atomic.NewBool(true),
		amInterested:         *atomic.NewBool(fast),
		peerChoking:          *atomic.NewBool(true),
		peerInterested:       *atomic.NewBool(fast),

		ourPieceRequests: make(chan uint32, 1),

		responseCond: sync.Cond{L: &gsync.EmptyLock{}},

		//ResChan:   make(chan req.Response, 1),
		myRequests:       xsync.NewMapOf[proto.ChunkRequest, time.Time](),
		myRequestHistory: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		Rejected: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		peerRequests: xsync.NewMapOf[proto.ChunkRequest, empty.Empty](),

		r: bufio.NewReaderSize(conn, units.KiB*18),
		w: bufio.NewWriterSize(conn, units.KiB*8),

		allowFast: bm.New(d.info.NumPieces),
		Requested: bm.New(d.info.NumPieces),

		rttAverage: sizedSlice[time.Duration]{
			limit: 2000,
		},
	}

	p.cancel = func() {
		p.log.Trace().Caller(1).Msg("cancel context")
		cancel()
	}

	if ua != "" {
		p.UserAgent.Store(&ua)
	}

	go p.start(skipHandshake)
	return p
}

var ErrPeerSendInvalidData = errors.New("addrPort send invalid data")

type Peer struct {
	log      zerolog.Logger
	ctx      context.Context
	Conn     net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	d        *Download
	lastSend atomic.Pointer[time.Time]
	cancel   context.CancelFunc
	Bitmap   *bm.Bitmap

	Requested *bm.Bitmap

	myRequests       *xsync.MapOf[proto.ChunkRequest, time.Time]
	myRequestHistory *xsync.MapOf[proto.ChunkRequest, empty.Empty]

	peerRequests *xsync.MapOf[proto.ChunkRequest, empty.Empty]

	Rejected  *xsync.MapOf[proto.ChunkRequest, empty.Empty]
	allowFast *bm.Bitmap
	ioOut     *flowrate.Monitor
	ioIn      *flowrate.Monitor
	UserAgent atomic.Pointer[string]

	ltDontHaveExtensionId atomic.Pointer[proto.ExtensionMessage]

	ourPieceRequests chan uint32 // our requests for peer chan

	responseCond sync.Cond
	Address      netip.AddrPort

	rttAverage sizedSlice[time.Duration]

	peerChoking    atomic.Bool
	peerInterested atomic.Bool
	amChoking      atomic.Bool
	amInterested   atomic.Bool
	QueueLimit     atomic.Uint32
	closed         atomic.Bool

	rttMutex                  sync.RWMutex
	wm                        sync.Mutex
	readBuf                   [4]byte // buffer for reading message size and event id
	Incoming                  bool
	supportFastExtension      bool
	supportExtensionHandshake bool
}

func (p *Peer) Response(res proto.ChunkResponse) {
	_, ok := p.peerRequests.LoadAndDelete(res.Request())
	if !ok {
		panic("send response without request")
	}
	p.ioOut.Update(len(res.Data))
	err := p.sendEvent(Event{
		Event: proto.Piece,
		Res:   res,
	})

	if err != nil {
		p.close()
	}
}

func (p *Peer) Request(req proto.ChunkRequest) {
	_, exist := p.myRequests.LoadOrStore(req, time.Now())
	if exist {
		p.log.Trace().Msg("myRequests already sent")
		return
	}

	err := p.sendEvent(Event{
		Event: proto.Request,
		Req:   req,
	})
	if err != nil {
		p.close()
	}
}

func (p *Peer) Have(index uint32) {
	err := p.sendEvent(Event{
		Index: index,
		Event: proto.Have,
	})
	if err != nil {
		p.close()
	}
}

func (p *Peer) close() {
	p.log.Trace().Caller(1).Msg("close")
	if p.closed.CompareAndSwap(false, true) {
		p.cancel()
		p.d.conn.Delete(p.Address)
		p.d.c.sem.Release(1)
		p.d.c.connectionCount.Sub(1)
		_ = p.Conn.Close()
	}
}

func (p *Peer) ourRequestHandle() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case index := <-p.ourPieceRequests:
			chunkLen := pieceChunkLen(p.d.info, index)
			for i := 0; i < chunkLen; i++ {
				if p.closed.Load() {
					return
				}

				// 10 is a magic number to avoid peer reject our requests
				for p.myRequests.Size() >= int(p.QueueLimit.Load())-10 {
					p.responseCond.Wait()
				}

				p.Request(pieceChunk(p.d.info, index, i))
			}

			p.d.scheduleRequest <- empty.Empty{}
		}
	}
}

func (p *Peer) start(skipHandshake bool) {
	p.log.Trace().Msg("start")
	defer p.close()

	if err := proto.SendHandshake(p.Conn, p.d.info.Hash, NewPeerID()); err != nil {
		p.log.Trace().Err(err).Msg("failed to send handshake to addrPort")
		return
	}

	if !skipHandshake {
		h, err := proto.ReadHandshake(p.Conn)
		if err != nil {
			p.log.Trace().Err(err).Msg("failed to read handshake")
			return
		}
		if h.InfoHash != p.d.info.Hash {
			p.log.Trace().Msgf("addrPort info hash mismatch %x", h.InfoHash)
			return
		}
		p.supportFastExtension = h.FastExtension
		p.log = p.log.With().Str("peer_id", url.QueryEscape(string(h.PeerID[:]))).Logger()
		p.log.Trace().Msg("connect to addrPort")
		ua := util.ParsePeerId(h.PeerID)
		p.UserAgent.Store(&ua)
	}

	if p.supportFastExtension {
		p.log.Trace().Msg("allow fast extensionHandshake")
	}

	// sync point, after both side send handshake and starting send peer messages

	go func() {
		err := p.sendInitPayload()
		if err != nil {
			p.close()
			return
		}
		p.keepAlive()
	}()

	// make it visible to download
	_, loaded := p.d.conn.LoadAndStore(p.Address, p)
	if loaded {
		panic("unexpected connected peer")
	}

	go p.ourRequestHandle()

	for {
		if p.ctx.Err() != nil {
			return
		}

		event, err := p.DecodeEvents()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.log.Trace().Err(err).Msg("failed to decode event")
			}
			return
		}

		if event.Ignored {
			continue
		}

		p.log.Trace().Msgf("receive %s", color.GreenString(event.Event.String()))

		switch event.Event {
		case proto.Bitfield:
			p.Bitmap.OR(event.Bitmap)
			if p.Bitmap.WithAndNot(p.d.bm).Count() != 0 {
				if p.amInterested.CompareAndSwap(false, true) {
					err = p.sendEvent(Event{Event: proto.Interested})
					if err != nil {
						return
					}
				}
			} else {
				if p.amInterested.CompareAndSwap(true, false) {
					err = p.sendEvent(Event{Event: proto.NotInterested})
					if err != nil {
						return
					}
				}
			}
		case proto.Have:
			p.Bitmap.Set(event.Index)
		case proto.Interested:
			p.peerInterested.Store(true)
		case proto.NotInterested:
			p.peerInterested.Store(false)
		case proto.Choke:
			p.peerChoking.Store(true)
		case proto.Unchoke:
			p.peerChoking.Store(false)
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
				p.log.Warn().Msg("failed to validate request, maybe malicious peers")
				return
			}

			p.peerRequests.Store(event.Req, empty.Empty{})
			p.d.scheduleRequest <- empty.Empty{}
		case proto.Extended:
			if event.ExtensionID == proto.ExtensionHandshake {
				p.log.Debug().Any("ext", event.ExtHandshake).Msg("receive handshake")

				if event.ExtHandshake.V.Set {
					p.UserAgent.Store(&event.ExtHandshake.V.Value)
				}
				if event.ExtHandshake.QueueLength.Set {
					p.QueueLimit.Store(event.ExtHandshake.QueueLength.Value)
				}

				if event.ExtHandshake.Main.LTDontHave.Set {
					p.ltDontHaveExtensionId.Store(&event.ExtHandshake.Main.LTDontHave.Value)
				}
				continue
			}

			if dontHave := p.ltDontHaveExtensionId.Load(); dontHave != nil {
				if event.ExtensionID == *dontHave {
					p.Bitmap.Unset(event.Index)
				}
				continue
			}

		// TODO
		case proto.Cancel:
		case proto.Port:
		case proto.Suggest:
		case proto.HaveAll:
			p.Bitmap.Fill()
		case proto.HaveNone:
			p.Bitmap.Clear()
		case proto.Reject:
			p.log.Debug().Msgf("reject %+v", event.Req)
			p.Rejected.Store(event.Req, empty.Empty{})
			p.myRequests.Delete(event.Req)
		case proto.AllowedFast:
			p.allowFast.Set(event.Index)
		// currently unsupported

		// currently ignored
		case proto.BitCometExtension:
		}

		//nolint:exhaustive
		switch event.Event {
		case proto.Have, proto.HaveAll, proto.Bitfield:
			p.d.buildNetworkPieces <- empty.Empty{}

			go func() {
				if p.Bitmap.WithAndNot(p.d.bm).Count() != 0 {
					err = p.sendEvent(Event{Event: proto.Interested})
					if err != nil {
						return
					}
				}

				// peer and us are both seeding, disconnect
				if p.Bitmap.Count() == p.d.info.NumPieces && p.d.GetState() == Seeding {
					p.cancel()
				}
			}()
		}
	}
}

func (p *Peer) sendInitPayload() error {
	var err error
	if p.supportFastExtension && p.d.bm.Count() == 0 {
		err = p.sendEvent(Event{Event: proto.HaveNone})
	} else if p.supportFastExtension && p.d.bm.Count() == p.d.info.NumPieces {
		err = p.sendEvent(Event{Event: proto.HaveAll})
	} else {
		err = p.sendEvent(Event{Event: proto.Bitfield, Bitmap: p.d.bm})
	}

	if err != nil {
		return err
	}

	if p.supportExtensionHandshake {
		return p.sendEvent(Event{Event: proto.Extended, ExtHandshake: extensionHandshake{
			V:           null.NewString(handshakeAgent),
			QueueLength: null.NewUint32(1000),
		}})
	}

	return nil
}

func (p *Peer) sendEvent(e Event) error {
	p.wm.Lock()
	defer p.wm.Unlock()

	err := p.write(e)
	if err != nil {
		return err
	}

	if e.Event != proto.Have {
		return p.w.Flush()
	}

	return nil
}

func (p *Peer) validateRequest(req proto.ChunkRequest) bool {
	if req.PieceIndex >= p.d.info.NumPieces {
		return false
	}

	expectedLen := as.Uint32(p.d.pieceLength(req.PieceIndex))

	return !(req.Begin+req.Length > expectedLen)
}

func (p *Peer) resIsValid(res proto.ChunkResponse) bool {
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

func (p *Peer) Unchoke() {
	err := p.sendEvent(Event{
		Event: proto.Unchoke,
	})
	if err != nil {
		p.close()
	}
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
