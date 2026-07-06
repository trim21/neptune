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
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"go.uber.org/atomic"

	"neptune/internal/core/tracker"
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

var peerIDPrefix string

func init() {
	if peerIDPrefix == "" {
		peerIDPrefix = "-NE" +
			string(version.MAJOR+'0') +
			string(version.MINOR+'0') +
			string(version.PATCH+'0') + "0-"
	}
}

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

		log:               l.Logger(),
		Conn:              conn,
		d:                 d,
		Bitmap:            bm.New(d.info.NumPieces),
		pieceUploadRate:   flowrate.New(time.Second, time.Second),
		pieceDownloadRate: flowrate.New(100*time.Millisecond, 100*time.Millisecond),
		Address:           addr,
		id:                d.c.peerIDCounter.Add(1),
		QueueLimit:        *atomic.NewUint32(2000),
		Incoming:          skipReadHandshake,

		ourChoking:     *atomic.NewBool(true),
		ourInterested:  *atomic.NewBool(false),
		peerChoking:    *atomic.NewBool(true),
		peerInterested: *atomic.NewBool(false),

		blockRequests: make(chan pieceBlock, 50),

		pieceDone:        make(chan struct{}, 1),
		desiredQueueSize: *atomic.NewInt32(1),

		UserAgent: *atomic.NewPointer(&ua),

		responseCond: gsync.NewCond(gsync.EmptyLock{}),

		//ResChan:   make(chan req.Response, 1),
		myRequests:       xsync.NewMap[proto.ChunkRequest, time.Time](),
		myRequestHistory: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		Rejected: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		peerRequests: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		r: bufio.NewReaderSize(d.ioDownloadRate.WrapReader(conn), units.KiB*18),
		w: bufio.NewWriterSize(conn, units.KiB*8),

		allowFast: bm.New(d.info.NumPieces),

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
	log               zerolog.Logger
	closeErr          error
	ctx               context.Context
	Conn              net.Conn
	lastSend          atomic.Time
	snubbedAt         atomic.Time
	lastUnchokeAt     atomic.Time
	pieceDownloadRate *flowrate.Monitor
	Bitmap            *bm.Bitmap
	myRequests        *xsync.Map[proto.ChunkRequest, time.Time]
	myRequestHistory  *xsync.Map[proto.ChunkRequest, empty.Empty]
	d                 *Download
	Rejected          *xsync.Map[proto.ChunkRequest, empty.Empty]
	allowFast         *bm.Bitmap
	peerRequests      *xsync.Map[proto.ChunkRequest, empty.Empty]
	cancel            context.CancelFunc
	UserAgent         atomic.Pointer[string]
	blockRequests     chan pieceBlock
	responseCond      *gsync.Cond
	peerID            atomic.Pointer[proto.PeerID]
	pieceDone         chan struct{}
	w                 *bufio.Writer
	r                 *bufio.Reader
	pieceUploadRate   *flowrate.Monitor
	Address           netip.AddrPort
	requestQueue      []pieceBlock
	rttAverage        sizedSlice[time.Duration]
	disconnecting     atomic.Bool
	isSeed            atomic.Bool
	QueueLimit        atomic.Uint32
	desiredQueueSize  atomic.Int32
	ourInterested     atomic.Bool
	snubbed           atomic.Bool
	closed            atomic.Bool
	peerInterested    atomic.Bool
	ourChoking        atomic.Bool
	preferred         atomic.Bool
	peerChoking       atomic.Bool
	endgame           atomic.Bool
	rttMutex          sync.RWMutex
	wm                sync.Mutex
	rqMu              sync.Mutex
	extDontHaveID     gsync.AtomicUint[proto.ExtensionMessage]
	extPexID          gsync.AtomicUint[proto.ExtensionMessage]
	id                uint32
	writeBuf          [4]byte
	readBuf           [4]byte
	Incoming          bool
	fastExtension     bool
	dhtEnabled        bool
	subExtensions     bool
	hadTransfer       bool
}

func (p *Peer) Response(res *proto.ChunkResponse) bool {
	p.hadTransfer = true
	_, ok := p.peerRequests.LoadAndDelete(res.Request())
	if !ok {
		// Request might be canceled concurrently (Cancel) or already served.
		return false
	}
	p.sendEventX(Event{
		Event: proto.Piece,
		Res:   res,
	})
	return true
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
		p.disconnecting.Store(true)
		p.log.Trace().Caller(1).Msg("close")

		// Decrement picker refcount for all pieces this peer had,
		// and abort any blocks we had requested from this peer.
		p.Bitmap.Range(func(u uint32) {
			p.d.picker.decRefcount(u)
		})
		p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
			bi := int(req.Begin / uint32(defaultBlockSize))
			p.d.picker.abortDownload(req.PieceIndex, bi)
			return true
		})

		// Record disconnect reason for future retry decisions.
		// Only record for outgoing peers; incoming peers don't need retry logic.
		if !p.Incoming {
			p.d.recordDisconnect(p.Address, p.hadTransfer, p.closeErr)
		}

		p.d.peers.Delete(p.Address)
		p.d.c.sem.Release(1)
		p.d.c.connectionCount.Sub(1)
		p.cancel()
		_ = p.Conn.Close()
		p.d.buildNetworkPieces <- empty.Empty{}

		// Signal connection loop to fill the freed slot.
		if p.d.HasState(Downloading | Seeding) {
			select {
			case p.d.pendingPeersSignal <- empty.Empty{}:
			default:
			}
		}

		// Signal scheduler: blocks freed by abortDownload are now available
		// for other peers to pick up immediately.
		select {
		case p.d.scheduleRequestSignal <- empty.Empty{}:
		default:
		}
	}
}

func (p *Peer) ourRequestHandle() {
	// Request initial blocks immediately after handshake completes.
	p.d.requestABlock(p)

	for {
		select {
		case <-p.ctx.Done():
			return
		case block := <-p.blockRequests:
			p.rqMu.Lock()
			p.requestQueue = append(p.requestQueue, block)
			p.rqMu.Unlock()
			p.sendBlockRequests()
		case <-p.pieceDone:
			// Piece completed, may need to request more
			p.sendBlockRequests()
			select {
			case p.d.scheduleRequestSignal <- empty.Empty{}:
			default:
			}
		}
	}
}

// sendBlockRequests drains the peer's requestQueue and sends wire requests
// up to the peer's queue limit. Mirrors libtorrent's send_block_requests().
func (p *Peer) sendBlockRequests() {
	if p.closed.Load() || p.isDisconnecting() {
		return
	}

	desiredSize := p.updateDesiredQueueSize()

	for {
		currentSize := p.myRequests.Size()
		if currentSize >= desiredSize {
			return
		}

		p.rqMu.Lock()
		if len(p.requestQueue) == 0 {
			p.rqMu.Unlock()
			return
		}
		block := p.requestQueue[0]
		p.requestQueue = p.requestQueue[1:]
		p.rqMu.Unlock()

		chunk := pieceChunk(p.d.info, block.pieceIndex, block.blockIndex)

		// Skip if peer is choking us (unless allowed fast)
		if p.peerChoking.Load() && !p.allowFast.Contains(block.pieceIndex) {
			p.d.picker.abortDownload(block.pieceIndex, block.blockIndex)
			continue
		}

		p.Request(chunk)
	}
}

// updateDesiredQueueSize computes the desired number of outstanding requests
// based on the peer's download rate. Mirrors libtorrent's update_desired_queue_size().
//
// Formula: desired = queue_time * download_rate / block_size
// Clamped between [minRequestQueue, maxRequestQueue].
// updateDesiredQueueSize returns the desired number of outstanding requests.
// Uses a fixed deep queue so peers are kept saturated; the global / per-torrent
// rate limiter handles actual throughput control via backpressure.
// Respects the peer's advertised queue limit as an upper bound.
func (p *Peer) updateDesiredQueueSize() int {
	if p.snubbed.Load() {
		return 1
	}

	if p.endgame.Load() {
		return 1
	}

	// Use half the peer's advertised limit so we don't dominate its slots.
	// Default QueueLimit is 2000, so typical size is 1000 (~16 MB in flight).
	peerLimit := int(p.QueueLimit.Load())
	if peerLimit <= 0 {
		peerLimit = 2000
	}
	queueSize := max(peerLimit/2, minRequestQueue)
	queueSize = min(queueSize, maxRequestQueue)

	p.desiredQueueSize.Store(int32(queueSize))
	return queueSize
}

// requestQueueLen returns the length of the request queue under lock.
func (p *Peer) requestQueueLen() int {
	p.rqMu.Lock()
	defer p.rqMu.Unlock()
	return len(p.requestQueue)
}

// isInQueue checks if a chunk is already in the peer's request queue or request set.
func (p *Peer) isInQueue(chunk proto.ChunkRequest) bool {
	if _, ok := p.myRequests.Load(chunk); ok {
		return true
	}

	p.rqMu.Lock()
	defer p.rqMu.Unlock()

	for _, b := range p.requestQueue {
		q := pieceChunk(p.d.info, b.pieceIndex, b.blockIndex)
		if q == chunk {
			return true
		}
	}

	return false
}

// setEndgame sets whether the peer is in endgame mode.
func (p *Peer) setEndgame(v bool) {
	p.endgame.Store(v)
}

// isDisconnecting returns true if the peer is in the process of disconnecting.
func (p *Peer) isDisconnecting() bool {
	return p.disconnecting.Load()
}

func (p *Peer) checkRequestTimeouts() {
	const pieceTimeout = 30 * time.Second
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			timedOut := false
			p.myRequests.Range(func(req proto.ChunkRequest, reqTime time.Time) bool {
				if now.Sub(reqTime) > pieceTimeout {
					timedOut = true
					return false // stop iterating
				}
				return true
			})

			if timedOut && !p.snubbed.Load() {
				p.snubbed.Store(true)
				p.snubbedAt.Store(now)
				p.log.Warn().Msg("peer snubbed: request timeout")

				// abort all pending downloads so other peers can pick them up
				p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
					bi := int(req.Begin / uint32(defaultBlockSize))
					p.d.picker.abortDownload(req.PieceIndex, bi)
					return true
				})

				// Clear myRequests — entries are now stale and would
				// inflate Size() preventing new requests.
				p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
					p.myRequests.Delete(req)
					return true
				})

				// Clear requestQueue — stale blocks would be resent on un-snub.
				p.rqMu.Lock()
				p.requestQueue = p.requestQueue[:0]
				p.rqMu.Unlock()

				// trigger reschedule
				select {
				case p.d.scheduleRequestSignal <- empty.Empty{}:
				default:
				}
			}
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

	// Register in persistent peer list for reconnect/backoff tracking.
	if p.Incoming {
		if p.d.peerList.addOrUpdateIncoming(p.Address, time.Now().Unix(), p) {
			// Duplicate — another connection to this addr exists.
			p.close()
			return
		}
	}

	go p.ourRequestHandle()
	go p.checkRequestTimeouts()

	for {
		if p.ctx.Err() != nil {
			return
		}

		event, err := p.DecodeEvents()
		if err != nil {
			p.log.Trace().Err(err).Msg("failed to decode event")
			p.closeErr = err
			return
		}

		if event.Ignored {
			continue
		}

		p.log.Trace().Stringer("event", event.Event).Msg("receive event")

		switch event.Event {
		case proto.Bitfield:
			p.Bitmap.OR(event.Bitmap)
			// Update picker: increment refcount for each piece the peer has
			event.Bitmap.Range(func(u uint32) {
				p.d.picker.incRefcount(u)
			})
			if !p.isSeed.Load() && p.Bitmap.Count() == p.d.info.NumPieces {
				p.isSeed.Store(true)
			}
		case proto.Have:
			if event.Index >= p.d.info.NumPieces {
				p.log.Debug().Uint32("index", event.Index).Msg("peer send 'Have' message with invalid index")
				return
			}

			p.Bitmap.Set(event.Index)
			p.d.picker.incRefcount(event.Index)
			if !p.isSeed.Load() && p.Bitmap.Count() == p.d.info.NumPieces {
				p.isSeed.Store(true)
			}
		case proto.Interested:
			p.peerInterested.Store(true)
			p.d.onPeerInterested(p)
			p.d.scheduleResponseSignal <- empty.Empty{}
		case proto.NotInterested:
			p.peerInterested.Store(false)
		case proto.Choke:
			p.peerChoking.Store(true)
		case proto.Unchoke:
			p.peerChoking.Store(false)
			p.d.scheduleRequestSignal <- empty.Empty{}
		case proto.Piece:
			p.hadTransfer = true
			if !p.resIsValid(event.Res) {
				p.log.Trace().Msg("failed to validate response")
				// send response without myRequests
				return
			}

			// Request more blocks for this peer immediately (libtorrent
			// calls request_a_block from incoming_piece).
			p.d.requestABlock(p)

			// Drain requestQueue in case requestABlock was unable to push
			// new blocks (e.g. numRequests already saturated).
			p.sendBlockRequests()

			p.responseCond.Signal()
			p.pieceDownloadRate.Update(len(event.Res.Data))

			if p.snubbed.Load() {
				p.snubbed.Store(false)
				p.desiredQueueSize.Store(1)
				p.log.Info().Msg("peer un-snubbed: responding again")
			}

			p.d.ResChan <- event.Res
		case proto.Request:
			if !p.validateRequest(event.Req) {
				if p.fastExtension {
					go p.sendEventX(Event{Req: event.Req, Event: proto.Reject})
				}
				break
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
				p.d.picker.decRefcount(event.Index)
				continue
			}

			continue
		case proto.HaveAll:
			// Decrement old pieces before replacing bitmap with full set
			p.Bitmap.Range(func(u uint32) {
				p.d.picker.decRefcount(u)
			})
			p.Bitmap.Fill()
			// Increment refcount for all pieces the peer now has
			for i := range p.d.info.NumPieces {
				p.d.picker.incRefcount(i)
			}
			p.isSeed.Store(true)
		case proto.HaveNone:
			// Decrement old pieces before clearing
			p.Bitmap.Range(func(u uint32) {
				p.d.picker.decRefcount(u)
			})
			p.Bitmap.Clear()
		case proto.Cancel:
			p.peerRequests.Delete(event.Req)
		case proto.Reject:
			p.log.Trace().Msgf("reject %+v", event.Req)
			p.Rejected.Store(event.Req, empty.Empty{})
			p.myRequests.Delete(event.Req)

			// Abort in the picker so other peers can request this block.
			bi := int(event.Req.Begin / uint32(defaultBlockSize))
			p.d.picker.abortDownload(event.Req.PieceIndex, bi)

			// Remove matching entry from requestQueue if present.
			p.rqMu.Lock()
			for i, b := range p.requestQueue {
				if b.pieceIndex == event.Req.PieceIndex &&
					int(event.Req.Begin/uint32(defaultBlockSize)) == b.blockIndex {
					p.requestQueue = append(p.requestQueue[:i], p.requestQueue[i+1:]...)
					break
				}
			}
			p.rqMu.Unlock()

			// Drain requestQueue to replace the rejected block.
			p.sendBlockRequests()
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
				QueueLength: null.NewUint32(2000),
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
	if ok {
		p.rttMutex.Lock()
		p.rttAverage.Push(time.Since(reqTime))
		p.rttMutex.Unlock()
	}

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
			addrPort:         tracker.ParseCompact4(msg.Added[i*6 : i*6+6]),
			preferEnc:        flag&proto.PexFlagPreferEnc != 0,
			seedOnly:         flag&proto.PexFlagSeedOnly != 0,
			supportUTP:       flag&proto.PexFlagSupportUTP != 0,
			supportHolePunch: flag&proto.PexFlagSupportHolePunchP != 0,
			outGoing:         flag&proto.PexFlagOutgoing != 0,
		})
	}

	for i, flag := range msg.Added6Flag {
		r = append(r, pexPeer{
			addrPort:         tracker.ParseCompact6(msg.Added6[i*18 : i*18+18]),
			preferEnc:        flag&proto.PexFlagPreferEnc != 0,
			seedOnly:         flag&proto.PexFlagSeedOnly != 0,
			supportUTP:       flag&proto.PexFlagSupportUTP != 0,
			supportHolePunch: flag&proto.PexFlagSupportHolePunchP != 0,
			outGoing:         flag&proto.PexFlagOutgoing != 0,
		})
	}

	var dropped = make([]netip.AddrPort, 0, len(msg.Dropped)/6+len(msg.Dropped6)/18)

	for i := 0; i < len(msg.Dropped); i += 6 {
		dropped = append(dropped, tracker.ParseCompact4(msg.Dropped[i:i+6]))
	}

	for i := 0; i < len(msg.Dropped6); i += 18 {
		dropped = append(dropped, tracker.ParseCompact6(msg.Dropped6[i:i+18]))
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
