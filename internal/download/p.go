// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

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

	"neptune/internal/client/tracker"
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

// pieceBlockQueue is a fixed-capacity FIFO matching the per-peer request cap.
// Normal push/pop operations never allocate or move existing entries.
type pieceBlockQueue struct {
	blocks [maxRequestQueue]PieceBlock
	head   int
	size   int
}

func (q *pieceBlockQueue) Len() int {
	return q.size
}

func (q *pieceBlockQueue) Push(block PieceBlock) bool {
	if q.size == len(q.blocks) {
		return false
	}

	tail := (q.head + q.size) % len(q.blocks)
	q.blocks[tail] = block
	q.size++
	return true
}

func (q *pieceBlockQueue) Front() (PieceBlock, bool) {
	if q.size == 0 {
		return PieceBlock{}, false
	}
	return q.blocks[q.head], true
}

func (q *pieceBlockQueue) Pop() {
	if q.size == 0 {
		return
	}

	q.head = (q.head + 1) % len(q.blocks)
	q.size--
	if q.size == 0 {
		q.head = 0
	}
}

func (q *pieceBlockQueue) Clear() {
	q.head = 0
	q.size = 0
}

func (q *pieceBlockQueue) Range(fn func(PieceBlock) bool) {
	for i := range q.size {
		if !fn(q.blocks[(q.head+i)%len(q.blocks)]) {
			return
		}
	}
}

func (q *pieceBlockQueue) Remove(pieceIndex uint32, blockIndex int) bool {
	for i := range q.size {
		index := (q.head + i) % len(q.blocks)
		block := q.blocks[index]
		if block.PieceIndex != pieceIndex || block.BlockIndex != blockIndex {
			continue
		}

		for j := i; j < q.size-1; j++ {
			current := (q.head + j) % len(q.blocks)
			next := (current + 1) % len(q.blocks)
			q.blocks[current] = q.blocks[next]
		}
		q.size--
		if q.size == 0 {
			q.head = 0
		}
		return true
	}
	return false
}

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

func NewOutgoingPeer(conn net.Conn, d *Download, addr netip.AddrPort, encrypted bool) Peer {
	return newPeer(conn, d, addr, false, nil, encrypted)
}

func NewIncomingPeer(conn net.Conn, d *Download, addr netip.AddrPort, h proto.Handshake, encrypted bool) Peer {
	return newPeer(conn, d, addr, true, &h, encrypted)
}

func newPeer(
	conn net.Conn,
	d *Download,
	addr netip.AddrPort,
	skipReadHandshake bool,
	h *proto.Handshake,
	encrypted bool,
) Peer {
	ctx, cancel := context.WithCancel(d.ctx)
	l := d.log.With().Stringer("addr", addr)
	var ua string
	if h != nil {
		ua = util.ParsePeerID(h.PeerID)
		l = l.Str("peer_id", url.QueryEscape(h.PeerID.AsString()))
	}

	p := &peerImpl{
		ctx:    ctx,
		cancel: cancel,

		log:               l.Logger(),
		Conn:              conn,
		d:                 d,
		peerCtx:           d.newPeerContext(),
		Bitmap:            bm.New(d.info.NumPieces),
		pieceUploadRate:   flowrate.New(time.Second, 5*time.Second),
		pieceDownloadRate: flowrate.New(time.Second, 5*time.Second),
		Address:           addr,
		connectedAt:       time.Now(),
		id:                d.peerIDCounter.Add(1),
		queueLimit:        *atomic.NewUint32(2000),
		incoming:          skipReadHandshake,
		encrypted:         encrypted,

		ourChoking:     *atomic.NewBool(true),
		ourInterested:  *atomic.NewBool(false),
		peerChoking:    *atomic.NewBool(true),
		peerInterested: *atomic.NewBool(false),

		desiredQueueSize: *atomic.NewInt32(1),

		userAgent: *atomic.NewPointer(&ua),

		responseCond:          gsync.NewCond(gsync.EmptyLock{}),
		scheduleRequestSignal: make(chan empty.Empty, 1),

		//ResChan:   make(chan req.Response, 1),
		myRequests:       xsync.NewMap[proto.ChunkRequest, time.Time](),
		myRequestHistory: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		Rejected: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		peerRequests: xsync.NewMap[proto.ChunkRequest, empty.Empty](),

		r: bufio.NewReaderSize(d.ioDownloadRate.WrapReader(conn), units.KiB*18),
		w: bufio.NewWriterSize(conn, units.KiB*8),

		allowFast: bm.New(d.info.NumPieces),

		contributedPieces:      bm.New(d.info.NumPieces),
		blockedPieces:          bm.NewLockFreeBitmap(d.info.NumPieces),
		suspectPieces:          bm.New(d.info.NumPieces),
		blockedPieceTimestamps: xsync.NewMap[uint32, time.Time](),

		peerID: *atomic.NewPointer(&proto.PeerID{}),

		rttAverage: sizedSlice[time.Duration]{limit: 2000},
		lastSend:   *atomic.NewInt64(time.Now().Unix()),
	}

	if h != nil {
		p.dhtEnabled = h.DhtEnabled
		p.subExtensions = h.ExchangeExtensions
		p.fastExtension = h.FastExtension
		p.peerID.Store(&h.PeerID)
	}

	go p.scheduleRequests()
	go p.start(skipReadHandshake)
	return p
}

var ErrPeerSendInvalidData = errors.New("addrPort send invalid data")

type peerImpl struct {
	log                    zerolog.Logger
	connectedAt            time.Time
	closeErr               atomic.Pointer[error]
	ctx                    context.Context
	Conn                   net.Conn
	blockedPieceTimestamps *xsync.Map[uint32, time.Time]
	userAgent              atomic.Pointer[string]
	d                      *Download
	peerCtx                *PeerContext
	cancel                 context.CancelFunc
	Bitmap                 *bm.Bitmap
	myRequests             *xsync.Map[proto.ChunkRequest, time.Time]
	myRequestHistory       *xsync.Map[proto.ChunkRequest, empty.Empty]
	lastPickDebug          atomic.Pointer[string]
	Rejected               *xsync.Map[proto.ChunkRequest, empty.Empty]
	allowFast              *bm.Bitmap
	peerRequests           *xsync.Map[proto.ChunkRequest, empty.Empty]
	pieceDownloadRate      *flowrate.Monitor
	suspectPieces          *bm.Bitmap
	responseCond           *gsync.Cond
	scheduleRequestSignal  chan empty.Empty
	peerID                 atomic.Pointer[proto.PeerID]
	w                      *bufio.Writer
	r                      *bufio.Reader
	pieceUploadRate        *flowrate.Monitor
	contributedPieces      *bm.Bitmap
	blockedPieces          *bm.LockFreeBitmap
	Address                netip.AddrPort
	lastPickResult         PickResult
	rttAverage             sizedSlice[time.Duration]
	requestQueue           pieceBlockQueue
	preferred              atomic.Bool
	snubbed                atomic.Bool
	onParole               atomic.Bool
	id                     uint64
	snubbedAt              atomic.Int64
	hashFails              atomic.Int32
	trustPoints            atomic.Int32
	disconnecting          atomic.Bool
	isSeed                 atomic.Bool
	queueLimit             atomic.Uint32
	lastSend               atomic.Int64
	ourInterested          atomic.Bool
	peerChoking            atomic.Bool
	closed                 atomic.Bool
	peerInterested         atomic.Bool
	ourChoking             atomic.Bool
	lastUnchokeAt          atomic.Int64
	desiredQueueSize       atomic.Int32
	rttMutex               sync.RWMutex
	wm                     sync.Mutex
	requestMu              sync.Mutex
	lastPickResultMu       sync.Mutex
	extDontHaveID          gsync.AtomicUint[proto.ExtensionMessage]
	extPexID               gsync.AtomicUint[proto.ExtensionMessage]
	readBuf                [4]byte
	writeBuf               [4]byte
	incoming               bool
	encrypted              bool
	fastExtension          bool
	dhtEnabled             bool
	subExtensions          bool
	hadTransfer            atomic.Bool
}

func (p *peerImpl) Response(res *proto.ChunkResponse) bool {
	p.hadTransfer.Store(true)
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

func (p *peerImpl) Request(req proto.ChunkRequest) {
	p.requestMu.Lock()
	if p.closed.Load() || p.isDisconnecting() || !p.reserveRequest(req) {
		p.requestMu.Unlock()
		return
	}
	p.requestMu.Unlock()

	p.sendRequest(req)
}

// reserveRequest records ownership of a picked block before it is removed from
// requestQueue. The caller must hold requestMu so Close cannot miss the ownership
// transfer and concurrent drains cannot overbook the request limit.
func (p *peerImpl) reserveRequest(req proto.ChunkRequest) bool {
	_, exist := p.myRequests.LoadOrStore(req, time.Now())
	if exist {
		p.log.Trace().Msg("myRequests already sent")
		return false
	}
	return true
}

func (p *peerImpl) sendRequest(req proto.ChunkRequest) {
	p.sendEventX(Event{
		Event: proto.Request,
		Req:   req,
	})
}

func (p *peerImpl) Have(index uint32) {
	p.sendEventX(Event{
		Index: index,
		Event: proto.Have,
	})
}

func (p *peerImpl) Unchoke() {
	p.sendEventX(Event{Event: proto.Unchoke})
}

func (p *peerImpl) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}

	p.disconnecting.Store(true)
	p.log.Trace().Caller(1).Msg("close")

	// Decrement picker refcount for all pieces this peer had,
	// and abort any blocks we had requested from this peer.
	p.Bitmap.Range(func(u uint32) {
		if !p.d.HasState(Seeding) {
			p.peerCtx.Picker().DecRefcount(u)
		}
	})
	// Synchronize with queue-to-request ownership transfers, then abort every
	// block that is still queued or in flight.
	p.requestMu.Lock()
	p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
		bi := int(req.Begin / uint32(defaultBlockSize))
		p.peerCtx.Picker().AbortDownload(req.PieceIndex, bi)
		return true
	})
	p.requestQueue.Range(func(block PieceBlock) bool {
		p.peerCtx.Picker().AbortDownload(block.PieceIndex, block.BlockIndex)
		return true
	})
	p.requestQueue.Clear()
	p.requestMu.Unlock()

	// Release exclusive piece ownerships so parole peers can claim them.
	p.d.picker.Load().ReleasePeerPieces(p.id)

	// Shared state cleanup: recordDisconnect handles connectedAddrs,
	// peerList, peer map, connection slot release, and re-request signal.
	p.d.recordDisconnect(p)

	p.cancel()
	_ = p.Conn.Close()

	// Signal remaining peers after every released request is visible in the
	// picker, but before this peer is removed from shared tracking.
	p.d.notifyPeersToRequest()
}

func (p *peerImpl) sendBlockRequests() {
	desiredSize := p.updateDesiredQueueSize()

	for {
		p.requestMu.Lock()

		if p.closed.Load() || p.isDisconnecting() ||
			p.requestQueue.Len() == 0 || p.myRequests.Size() >= desiredSize {
			p.requestMu.Unlock()
			return
		}

		block, _ := p.requestQueue.Front()
		chunk := pieceChunk(p.d.info, block.PieceIndex, block.BlockIndex)

		// Skip if peer is choking us (unless allowed fast).
		if p.peerChoking.Load() && !p.allowFast.Contains(block.PieceIndex) {
			p.requestQueue.Pop()
			p.requestMu.Unlock()
			p.peerCtx.Picker().AbortDownload(block.PieceIndex, block.BlockIndex)
			continue
		}

		// Reserve the request before removing it from the queue. Holding requestMu
		// keeps Close from missing it and serializes the queue-size check
		// across concurrent sendBlockRequests calls.
		if !p.reserveRequest(chunk) {
			p.requestQueue.Pop()
			p.requestMu.Unlock()
			continue
		}
		p.requestQueue.Pop()
		p.requestMu.Unlock()

		p.sendRequest(chunk)
	}
}

// requestBlocks is called from the peer event loop to fill the download pipeline.
// It is the peer's self-driven entrypoint to the global piece picker:
// when we learn about new pieces (Bitfield/Have), get unchoked, or receive data
// (Piece), we immediately ask the picker for more blocks and flush them to the wire.
func (p *peerImpl) requestBlocks() {
	if p.closed.Load() || p.isDisconnecting() {
		return
	}
	if !p.d.IsActiveDownloading() {
		return
	}
	// If choked and no allowed-fast pieces, nothing to request.
	if p.peerChoking.Load() && p.allowFast.Count() == 0 {
		return
	}

	p.requestABlock()
}

// requestABlock signals the peer's scheduler. The capacity-one channel
// coalesces concurrent triggers without blocking their callers.
func (p *peerImpl) requestABlock() {
	select {
	case p.scheduleRequestSignal <- empty.Empty{}:
	default:
	}
}

// scheduleRequests is the single per-peer scheduling goroutine. It serializes
// picker access for this peer and exits with the peer context.
func (p *peerImpl) scheduleRequests() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.scheduleRequestSignal:
			p.requestABlockOnce()
		}
	}
}

func (p *peerImpl) requestABlockOnce() {
	if p.closed.Load() || !p.peerCtx.IsDownloading() {
		return
	}
	picker := p.peerCtx.Picker()

	desired := p.DesiredQueueSize()
	outstanding := p.OutstandingRequests()
	queued := p.QueueLen()

	pickResult := picker.RequestABlock(
		p.LastPickResult(),
		desired,
		outstanding,
		queued,
		p.IsChoking(),
		p.PeerBitmap(),
		p.FastBitmap(),
		p.blockedPieces,
		p.onParole.Load(),
		p.id,
	)
	p.SetLastPickResult(pickResult)
	free := pickResult.FreeBlocks
	busy := pickResult.BusyBlocks

	if len(free) == 0 && len(busy) == 0 {
		if p.d.session.Debug {
			s := fmt.Sprintf("skip: numReq=%d (desired=%d, myReq=%d, reqQ=%d), free=%d busy=%d",
				desired-outstanding-queued, desired, outstanding, queued, len(free), len(busy))
			p.SetLastPickDebug(s)
		}
		return
	}

	// Free blocks: atomically transfer each picked block into this peer's
	// request queue. The picker state and peer queue are updated while holding
	// requestMu so Close cannot leave an orphaned Requested block behind.
	for _, fb := range free {
		p.tryEnqueuePickedBlock(picker, fb, false)
	}

	p.SendBlockRequests()

	// Busy blocks (endgame): only when no free blocks available, at most one to avoid burst.
	if len(free) == 0 {
		for _, bb := range busy {
			if !p.tryEnqueuePickedBlock(picker, bb, true) {
				continue
			}
			p.SendBlockRequests()
			return
		}
	}
}

func (p *peerImpl) tryEnqueuePickedBlock(picker *PiecePicker, block PieceBlock, retry bool) bool {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()

	if p.closed.Load() || p.isDisconnecting() {
		return false
	}

	chunk := pieceChunk(p.d.info, block.PieceIndex, block.BlockIndex)
	if _, ok := p.myRequests.Load(chunk); ok {
		return false
	}
	alreadyQueued := false
	p.requestQueue.Range(func(queued PieceBlock) bool {
		alreadyQueued = queued == block
		return !alreadyQueued
	})
	if alreadyQueued {
		return false
	}

	if !picker.TryMarkAsRequesting(block.PieceIndex, block.BlockIndex, retry) {
		return false
	}
	if !p.requestQueue.Push(block) {
		picker.AbortDownload(block.PieceIndex, block.BlockIndex)
		return false
	}
	return true
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
func (p *peerImpl) updateDesiredQueueSize() int {
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
	peerLimit := int(p.queueLimit.Load())
	if peerLimit > 0 {
		desired = min(desired, peerLimit/2)
	}

	p.desiredQueueSize.Store(int32(desired))
	return desired
}

// requestQueueLen returns the length of the request queue under lock.
func (p *peerImpl) requestQueueLen() int {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()
	return p.requestQueue.Len()
}

// isInQueue checks if a chunk is already in the peer's request queue or request set.
func (p *peerImpl) IsInQueue(chunk proto.ChunkRequest) bool {
	p.requestMu.Lock()
	defer p.requestMu.Unlock()

	if _, ok := p.myRequests.Load(chunk); ok {
		return true
	}

	found := false
	p.requestQueue.Range(func(block PieceBlock) bool {
		found = pieceChunk(p.d.info, block.PieceIndex, block.BlockIndex) == chunk
		return !found
	})
	return found
}

// isDisconnecting returns true if the peer is in the process of disconnecting.
func (p *peerImpl) isDisconnecting() bool {
	return p.disconnecting.Load()
}

func (p *peerImpl) lastPickDebugString() string {
	if s := p.lastPickDebug.Load(); s != nil {
		return *s
	}
	return "-"
}

func (p *peerImpl) checkRequestTimeouts() {
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
					p.peerCtx.Picker().AbortDownload(req.PieceIndex, bi)
					p.requestMu.Lock()
					p.myRequests.Delete(req)
					p.requestMu.Unlock()
				}

				consecutiveTimeouts += len(timedOutReqs)

				// Snub only after repeated consecutive timeouts (>= snubThreshold).
				if consecutiveTimeouts >= snubThreshold && !p.snubbed.Load() {
					p.snubbed.Store(true)
					p.snubbedAt.Store(now.Unix())
					p.log.Trace().Int("consecutive", consecutiveTimeouts).Msg("peer snubbed: repeated timeouts")

					// Clear all remaining in-flight and queued requests on snub.
					p.requestMu.Lock()
					p.myRequests.Range(func(req proto.ChunkRequest, _ time.Time) bool {
						bi := int(req.Begin / uint32(defaultBlockSize))
						p.peerCtx.Picker().AbortDownload(req.PieceIndex, bi)
						p.myRequests.Delete(req)
						return true
					})
					p.requestQueue.Clear()
					p.requestMu.Unlock()
				}

				// Trigger reschedule so other peers can take over the freed blocks.
				p.d.notifyPeersToRequest()
			} else {
				// No timeouts this tick — auto un-snub if we were previously snubbed.
				if consecutiveTimeouts < snubThreshold {
					consecutiveTimeouts = 0
				}
				if p.snubbed.Load() {
					p.snubbed.Store(false)
					p.desiredQueueSize.Store(1)
					p.log.Debug().Msg("peer un-snubbed: no recent timeouts")
					consecutiveTimeouts = 0
				}
			}
		}
	}
}

func (p *peerImpl) start(skipHandshake bool) {
	p.log.Trace().Msg("start")
	defer p.Close()

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
		p.userAgent.Store(&ua)
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

		defer p.Close()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-timer.C:
				if time.Now().Unix()-p.lastSend.Load() >= 60 {
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
		p.log.Trace().Uint64("existing_peer_id", actual.ID()).Msg("duplicate connection, closing")
		return
	}

	go p.checkRequestTimeouts()

	// for re-use
	var event Event

	for {
		if p.ctx.Err() != nil {
			return
		}

		err := p.decodeEvents(&event)
		if err != nil {
			p.log.Trace().Err(err).Msg("failed to decode event")
			p.closeErr.Store(&err)
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
					p.peerCtx.Picker().IncRefcount(u)
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
				p.peerCtx.Picker().IncRefcount(event.Index)
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
			p.hadTransfer.Store(true)
			if !p.resIsValid(event.Res) {
				p.log.Trace().Msg("failed to validate response")
				// send response without myRequests
				return
			}

			p.contributedPieces.Set(event.Res.PieceIndex)
			p.responseCond.Signal()
			p.pieceDownloadRate.Update(len(event.Res.Data))

			if p.snubbed.Load() {
				p.snubbed.Store(false)
				p.desiredQueueSize.Store(1)
				p.log.Debug().Msg("peer un-snubbed: responding again")
			}

			select {
			case p.d.resChan <- chunkSubmit{res: event.Res, peerID: p.id}:
			case <-p.ctx.Done():
				proto.PiecePool.Put(event.Res)
				return
			}
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
					v := event.ExtHandshake.V.Value // copy to avoid pointing into reused event struct
					p.userAgent.Store(&v)
				}
				if event.ExtHandshake.QueueLength.Set {
					p.queueLimit.Store(event.ExtHandshake.QueueLength.Value)
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
				added, _, err := parsePex(event.ExtPex)
				if err != nil {
					return
				}
				state := p.d.GetState()
				dp := make([]tracker.DiscoveredPeer, 0, len(added))
				for _, peer := range added {
					if !peer.outGoing {
						continue
					}
					if state == Seeding && peer.seedOnly {
						continue
					}
					dp = append(dp, tracker.DiscoveredPeer{AddrPort: peer.addrPort, Source: tracker.PeerSourcePEX})
				}
				if len(dp) > 0 {
					p.d.peersCh <- dp
				}
				continue
			}

			if event.ExtensionID == p.extDontHaveID.Load() {
				p.Bitmap.Unset(event.Index)
				if !p.d.HasState(Seeding) {
					p.peerCtx.Picker().DecRefcount(event.Index)
				}
				continue
			}

			continue
		case proto.HaveAll:
			// Decrement old pieces before replacing bitmap with full set
			p.Bitmap.Range(func(u uint32) {
				if !p.d.HasState(Seeding) {
					p.peerCtx.Picker().DecRefcount(u)
				}
			})
			p.Bitmap.Fill()
			if !p.d.HasState(Seeding) {
				for i := range p.d.info.NumPieces {
					p.peerCtx.Picker().IncRefcount(i)
				}
			}
			p.isSeed.Store(true)
			p.requestBlocks()
		case proto.HaveNone:
			// Decrement old pieces before clearing
			p.Bitmap.Range(func(u uint32) {
				if !p.d.HasState(Seeding) {
					p.peerCtx.Picker().DecRefcount(u)
				}
			})
			p.Bitmap.Clear()
		case proto.Cancel:
			p.peerRequests.Delete(event.Req)
		case proto.Reject:
			p.log.Trace().Msgf("reject %+v", event.Req)
			p.Rejected.Store(event.Req, empty.Empty{})
			bi := int(event.Req.Begin / uint32(defaultBlockSize))
			p.requestMu.Lock()
			p.myRequests.Delete(event.Req)
			p.requestQueue.Remove(event.Req.PieceIndex, bi)
			p.requestMu.Unlock()

			// Abort in the picker so other peers can request this block.
			p.peerCtx.Picker().AbortDownload(event.Req.PieceIndex, bi)

			// Libtorrent: request_a_block if queue is empty, then send_block_requests.
			p.requestABlock()
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
			p.d.session.DHT.AddPeer(netip.AddrPortFrom(p.Address.Addr(), event.Port))
		case proto.Suggest:
		// currently ignored and unsupported
		case proto.BitCometExtension:
		}

		switch event.Event {
		case proto.Have, proto.HaveAll, proto.Bitfield:
			if p.Bitmap.WithAndNot(p.d.completedBm).Count() != 0 {
				if p.ourInterested.CompareAndSwap(false, true) {
					go p.sendEventX(Event{Event: proto.Interested})
				}
			}
		}
	}
}

func (p *peerImpl) sendInitPayload() {
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
		p.Close()
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

func (p *peerImpl) sendEventX(e Event) {
	if p.sendEvent(e) != nil {
		p.Close()
	}
}

func (p *peerImpl) sendEvent(e Event) error {
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

func (p *peerImpl) validateRequest(req proto.ChunkRequest) bool {
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

func (p *peerImpl) resIsValid(res *proto.ChunkResponse) bool {
	r := proto.ChunkRequest{
		PieceIndex: res.PieceIndex,
		Begin:      res.Begin,
		Length:     as.Uint32(len(res.Data)),
	}

	p.requestMu.Lock()
	reqTime, ok := p.myRequests.LoadAndDelete(r)
	p.requestMu.Unlock()
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
