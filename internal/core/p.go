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

func NewOutgoingPeer(conn net.Conn, d *Download, addr netip.AddrPort, encrypted bool) *Peer {
	return newPeer(conn, d, addr, false, nil, encrypted)
}

func NewIncomingPeer(conn net.Conn, d *Download, addr netip.AddrPort, h proto.Handshake, encrypted bool) *Peer {
	return newPeer(conn, d, addr, true, &h, encrypted)
}

func newPeer(
	conn net.Conn,
	d *Download,
	addr netip.AddrPort,
	skipReadHandshake bool,
	h *proto.Handshake,
	encrypted bool,
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
		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),
		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		Address:           addr,
		id:                d.peerIDCounter.Add(1),
		QueueLimit:        *atomic.NewUint32(2000),
		Incoming:          skipReadHandshake,
		Encrypted:         encrypted,

		ourChoking:     *atomic.NewBool(true),
		ourInterested:  *atomic.NewBool(false),
		peerChoking:    *atomic.NewBool(true),
		peerInterested: *atomic.NewBool(false),

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
	lastPickDebug     atomic.Pointer[string]
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
	responseCond      *gsync.Cond
	peerID            atomic.Pointer[proto.PeerID]
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
	rttMutex          sync.RWMutex
	wm                sync.Mutex
	rqMu              sync.Mutex
	extDontHaveID     gsync.AtomicUint[proto.ExtensionMessage]
	extPexID          gsync.AtomicUint[proto.ExtensionMessage]
	id                uint64
	writeBuf          [4]byte
	readBuf           [4]byte
	Incoming          bool
	Encrypted         bool
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
		// These operate on p's own data; always safe.
		p.Bitmap.Range(func(u uint32) {
			if !p.d.HasState(Seeding) {
				p.d.picker.decRefcount(u)
			}
		})
		p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
			bi := int(req.Begin / uint32(defaultBlockSize))
			p.d.picker.abortDownload(req.PieceIndex, bi)
			return true
		})

		// Shared state cleanup: recordDisconnect checks whether we are the
		// primary peer (in connectedAddrs) and only then updates peerList.
		p.d.recordDisconnect(p)

		// peers map: keyed by unique peer ID, safe to always delete.
		p.d.peers.Delete(p.id)

		// Per-connection resources: we own one slot acquired before peer creation.
		p.d.c.sem.Release(1)
		p.d.c.connectionCount.Sub(1)

		p.cancel()
		_ = p.Conn.Close()

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

// requestBlocks is called from the peer event loop to fill the download pipeline.
// It is the peer's self-driven entrypoint to the global piece picker:
// when we learn about new pieces (Bitfield/Have), get unchoked, or receive data
// (Piece), we immediately ask the picker for more blocks and flush them to the wire.
func (p *Peer) requestBlocks() {
	if p.closed.Load() || p.isDisconnecting() {
		return
	}
	if !p.d.HasState(Downloading) {
		return
	}
	// If choked and no allowed-fast pieces, nothing to request.
	if p.peerChoking.Load() && p.allowFast.Count() == 0 {
		return
	}
	p.d.requestABlock(p)
}

// updateDesiredQueueSize computes the desired number of outstanding requests
// based on the peer's download rate. Mirrors libtorrent's update_desired_queue_size().
//
// Formula: desired = queue_time * download_rate / block_size
// Clamped between [minRequestQueue, maxRequestQueue].
// updateDesiredQueueSize computes the desired number of outstanding requests
// updateDesiredQueueSize computes the desired number of outstanding requests
// based on the peer's download rate. Fast peers get a deeper pipeline so they
// stay saturated; slow peers naturally get fewer slots.
//
// Formula: desired = downloadRate * queueTime / blockSize
// Clamped between [minRequestQueue, maxRequestQueue] and capped by peer's
// advertised queue limit.
func (p *Peer) updateDesiredQueueSize() int {
	if p.snubbed.Load() {
		return 1
	}

	// Rate-based pipeline sizing: aim for ~30 seconds of data in flight.
	const queueTime = 30 // seconds
	rate := p.pieceDownloadRate.Status().CurRate
	desired := int(float64(rate) * queueTime / float64(defaultBlockSize))
	desired = max(desired, minRequestQueue)
	desired = min(desired, maxRequestQueue)

	// Respect the peer's advertised queue limit as an upper bound.
	peerLimit := int(p.QueueLimit.Load())
	if peerLimit > 0 {
		desired = min(desired, peerLimit/2)
	}

	p.desiredQueueSize.Store(int32(desired))
	return desired
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

// isDisconnecting returns true if the peer is in the process of disconnecting.
func (p *Peer) isDisconnecting() bool {
	return p.disconnecting.Load()
}

func (p *Peer) lastPickDebugString() string {
	if s := p.lastPickDebug.Load(); s != nil {
		return *s
	}
	return "-"
}

func (p *Peer) checkRequestTimeouts() {
	const blockTimeout = 30 * time.Second
	const snubThreshold = 5 // consecutive timeouts before snubbing

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	consecutiveTimeouts := 0

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()

			// Collect timed-out block requests (per-block, not all-or-nothing).
			var timedOutReqs []proto.ChunkRequest
			p.myRequests.Range(func(req proto.ChunkRequest, reqTime time.Time) bool {
				if now.Sub(reqTime) > blockTimeout {
					timedOutReqs = append(timedOutReqs, req)
				}
				return true
			})

			if len(timedOutReqs) > 0 {
				// Abort each timed-out block individually so other peers can pick them up.
				for _, req := range timedOutReqs {
					bi := int(req.Begin / uint32(defaultBlockSize))
					p.d.picker.abortDownload(req.PieceIndex, bi)
					p.myRequests.Delete(req)
				}

				consecutiveTimeouts += len(timedOutReqs)

				// Snub only after repeated consecutive timeouts (>= snubThreshold).
				if consecutiveTimeouts >= snubThreshold && !p.snubbed.Load() {
					p.snubbed.Store(true)
					p.snubbedAt.Store(now)
					p.log.Warn().Int("consecutive", consecutiveTimeouts).Msg("peer snubbed: repeated timeouts")

					// Clear all remaining in-flight requests on snub.
					p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
						bi := int(req.Begin / uint32(defaultBlockSize))
						p.d.picker.abortDownload(req.PieceIndex, bi)
						p.myRequests.Delete(req)
						return true
					})

					// Clear requestQueue — stale blocks would be resent on un-snub.
					p.rqMu.Lock()
					p.requestQueue = p.requestQueue[:0]
					p.rqMu.Unlock()
				}

				// Trigger reschedule so other peers can take over the freed blocks.
				select {
				case p.d.scheduleRequestSignal <- empty.Empty{}:
				default:
				}
			} else {
				// No timeouts this tick — auto un-snub if we were previously snubbed.
				if consecutiveTimeouts < snubThreshold {
					consecutiveTimeouts = 0
				}
				if p.snubbed.Load() {
					p.snubbed.Store(false)
					p.desiredQueueSize.Store(1)
					p.log.Info().Msg("peer un-snubbed: no recent timeouts")
					consecutiveTimeouts = 0
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

	// Register in peers map by unique ID (never collides).
	p.d.peers.Store(p.id, p)

	// Address dedup: ensure only one peer per address.
	actual, loaded := p.d.connectedAddrs.LoadOrStore(p.Address, p)
	if loaded {
		// Another peer already owns this address. We lost the race.
		// defer close() handles cleanup; it will see we are not in connectedAddrs
		// and skip shared-state cleanup.
		p.log.Trace().Uint64("existing_peer_id", actual.id).Msg("duplicate connection, closing")
		return
	}

	// Register in persistent peer list for reconnect/backoff tracking.
	if p.Incoming {
		if p.d.peerList.addOrUpdateIncoming(p.Address, time.Now().Unix(), p) {
			// peerList already has a connection for this address.
			// We need to back out; close() will clean up shared state
			// because we are the primary in connectedAddrs.
			p.close()
			return
		}
	}

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
				if !p.d.HasState(Seeding) {
					p.d.picker.incRefcount(u)
				}
			})
			if !p.isSeed.Load() && p.Bitmap.Count() == p.d.info.NumPieces {
				p.isSeed.Store(true)
			}
			// Peer now has pieces we know about — ask for blocks immediately.
			p.requestBlocks()
		case proto.Have:
			if event.Index >= p.d.info.NumPieces {
				p.log.Debug().Uint32("index", event.Index).Msg("peer send 'Have' message with invalid index")
				return
			}

			p.Bitmap.Set(event.Index)
			if !p.d.HasState(Seeding) {
				p.d.picker.incRefcount(event.Index)
			}
			if !p.isSeed.Load() && p.Bitmap.Count() == p.d.info.NumPieces {
				p.isSeed.Store(true)
			}
			p.requestBlocks()
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
			p.requestBlocks()
		case proto.Piece:
			p.hadTransfer = true
			if !p.resIsValid(event.Res) {
				p.log.Trace().Msg("failed to validate response")
				// send response without myRequests
				return
			}

			// Request more blocks for this peer immediately (libtorrent
			// calls request_a_block from incoming_piece).
			p.requestBlocks()

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
				if !p.d.HasState(Seeding) {
					p.d.picker.decRefcount(event.Index)
				}
				continue
			}

			continue
		case proto.HaveAll:
			// Decrement old pieces before replacing bitmap with full set
			p.Bitmap.Range(func(u uint32) {
				if !p.d.HasState(Seeding) {
					p.d.picker.decRefcount(u)
				}
			})
			p.Bitmap.Fill()
			if !p.d.HasState(Seeding) {
				for i := range p.d.info.NumPieces {
					p.d.picker.incRefcount(i)
				}
			}
			p.isSeed.Store(true)
			select {
			case p.d.scheduleRequestSignal <- empty.Empty{}:
			default:
			}
		case proto.HaveNone:
			// Decrement old pieces before clearing
			p.Bitmap.Range(func(u uint32) {
				if !p.d.HasState(Seeding) {
					p.d.picker.decRefcount(u)
				}
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

			// Libtorrent: request_a_block if queue is empty, then send_block_requests.
			p.d.requestABlock(p)
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

			if p.Bitmap.WithAndNot(p.d.completedBm).Count() != 0 {
				if p.ourInterested.CompareAndSwap(false, true) {
					go p.sendEventX(Event{Event: proto.Interested})
				}
			}
		}
	}
}

func (p *Peer) sendInitPayload() {
	bitmapCount := p.d.completedBm.Count()

	var err error
	switch {
	case p.fastExtension && bitmapCount == 0:
		err = p.sendEvent(Event{Event: proto.HaveNone})
	case p.fastExtension && bitmapCount == p.d.info.NumPieces:
		err = p.sendEvent(Event{Event: proto.HaveAll})
	case bitmapCount != 0:
		err = p.sendEvent(Event{Event: proto.Bitfield, Bitmap: p.d.completedBm})
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

	pieceSize := as.Uint32(p.d.info.PieceLen(req.PieceIndex))

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
