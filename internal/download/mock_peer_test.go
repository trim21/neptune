// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"net/netip"
	"sync"
	"time"

	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/flowrate"
	"neptune/internal/proto"
)

// mockPeer is a controllable Peer implementation for testing download scheduling.
// Default values produce a sane, usable peer (not closed, not choking, empty bitmap).
type mockPeer struct {
	connectedAt            time.Time
	closeErr               error
	closed                 *atomic.Bool
	blockedPieces          *bm.LockFreeBitmap
	isSeed                 *atomic.Bool
	snubbed                *atomic.Bool
	onParole               *atomic.Bool
	peerInterested         *atomic.Bool
	ourChoking             *atomic.Bool
	ourInterested          *atomic.Bool
	peerChoking            *atomic.Bool
	inQueueMap             map[proto.ChunkRequest]bool
	fastBitmap             *bm.Bitmap
	bitmap                 *bm.Bitmap
	disconnecting          *atomic.Bool
	suspectPieces          *bm.Bitmap
	blockedPieceTimestamps map[uint32]time.Time
	lastUnchokeAt          *atomic.Int64
	peerRequests           map[proto.ChunkRequest]empty.Empty
	responseFunc           func(res *proto.ChunkResponse) bool
	resChan                chan chunkSubmit
	dl                     *Download
	preferred              *atomic.Bool
	addr                   netip.AddrPort
	userAgent              string
	peerIDString           string
	lastPickDebug          string
	lastPickRes            PickResult
	queued                 []PieceBlock
	enqueuedBlocks         []PieceBlock
	requestsSent           []proto.ChunkRequest
	info                   meta.Info
	uploadRate             flowrate.Monitor
	downloadRate           flowrate.Monitor
	downloadTotal          int64
	trustPoints            atomic.Int32
	peerID                 uint64
	sendBlockCalled        int
	reqMu                  sync.Mutex
	mu                     sync.Mutex
	queueLimit             uint32
	desiredSize            int32
	outstanding            int32
	hashFails              int32
	encrypted              bool
	subExtension           bool
	hadTrans               bool
	fastExtension          bool
	dhtEnabled             bool
	closedCalled           bool
	incoming               bool
}

func newMockPeer() *mockPeer {
	return &mockPeer{
		peerID:                 1,
		addr:                   netip.MustParseAddrPort("127.0.0.1:6881"),
		connectedAt:            time.Now(),
		closed:                 atomic.NewBool(false),
		disconnecting:          atomic.NewBool(false),
		isSeed:                 atomic.NewBool(false),
		snubbed:                atomic.NewBool(false),
		onParole:               atomic.NewBool(false),
		peerInterested:         atomic.NewBool(false),
		ourChoking:             atomic.NewBool(false),
		ourInterested:          atomic.NewBool(false),
		peerChoking:            atomic.NewBool(false),
		preferred:              atomic.NewBool(false),
		lastUnchokeAt:          atomic.NewInt64(time.Now().Unix()),
		bitmap:                 bm.New(0),
		fastBitmap:             bm.New(0),
		blockedPieces:          bm.NewLockFreeBitmap(0),
		suspectPieces:          bm.New(0),
		blockedPieceTimestamps: make(map[uint32]time.Time),
		downloadRate:           *flowrate.New(time.Second, time.Second),
		uploadRate:             *flowrate.New(time.Second, time.Second),
		desiredSize:            4,
		queueLimit:             2000,
		peerRequests:           make(map[proto.ChunkRequest]empty.Empty),
		enqueuedBlocks:         make([]PieceBlock, 0),
		requestsSent:           make([]proto.ChunkRequest, 0),
		inQueueMap:             make(map[proto.ChunkRequest]bool),
	}
}

// ── Test control methods (not part of PeerInterface) ─────────────────

func (m *mockPeer) setChoking(v bool)    { m.peerChoking.Store(v) }
func (m *mockPeer) setClosed(v bool)     { m.closed.Store(v) }
func (m *mockPeer) setOutstanding(n int) { m.atomicSetInt32(&m.outstanding, int32(n)) }
func (m *mockPeer) setDesiredSize(n int) { m.desiredSize = int32(n) }
func (m *mockPeer) setNumPieces(n uint32) {
	m.bitmap = bm.New(n)
	m.fastBitmap = bm.New(n)
	m.blockedPieces = bm.NewLockFreeBitmap(n)
}

func (m *mockPeer) addToQueue(chunk proto.ChunkRequest) {
	m.inQueueMap[chunk] = true
}

func (m *mockPeer) atomicSetInt32(target *int32, v int32) { *target = v }

// ── Identity ────────────────────────────────────────────────────────

func (m *mockPeer) ID() uint64           { return m.peerID }
func (m *mockPeer) Addr() netip.AddrPort { return m.addr }
func (m *mockPeer) Incoming() bool       { return m.incoming }

// ── Lifecycle ────────────────────────────────────────────────────────

func (m *mockPeer) Close() {
	if m.closed.Swap(true) {
		return // already closed
	}
	// Abort enqueued blocks in the picker, matching peerImpl.Close() behavior.
	if m.dl != nil {
		m.mu.Lock()
		for _, b := range m.queued {
			m.dl.picker.Load().AbortDownload(b.PieceIndex, b.BlockIndex)
		}
		m.mu.Unlock()
		// Decrement picker refcount for all pieces this peer had.
		m.bitmap.Range(func(u uint32) {
			m.dl.picker.Load().DecRefcount(u)
		})
		m.dl.picker.Load().ReleasePeerPieces(m.peerID)
	}
	m.closedCalled = true
}
func (m *mockPeer) Closed() bool          { return m.closed.Load() }
func (m *mockPeer) IsDisconnecting() bool { return m.disconnecting.Load() }
func (m *mockPeer) CloseError() error     { return m.closeErr }

// ── Piece availability ──────────────────────────────────────────────

func (m *mockPeer) PeerBitmap() *bm.Bitmap { return m.bitmap }
func (m *mockPeer) FastBitmap() *bm.Bitmap { return m.fastBitmap }
func (m *mockPeer) IsSeed() bool           { return m.isSeed.Load() }
func (m *mockPeer) PieceCount() uint32     { return m.bitmap.Count() }

// ── Choke / interest state ──────────────────────────────────────────

func (m *mockPeer) IsChoking() bool               { return m.peerChoking.Load() }
func (m *mockPeer) IsOurChoking() bool            { return m.ourChoking.Load() }
func (m *mockPeer) IsPeerInterested() bool        { return m.peerInterested.Load() }
func (m *mockPeer) IsOurInterested() bool         { return m.ourInterested.Load() }
func (m *mockPeer) IsSnubbed() bool               { return m.snubbed.Load() }
func (m *mockPeer) IsPreferred() bool             { return m.preferred.Load() }
func (m *mockPeer) AllowedFast(index uint32) bool { return m.fastBitmap.Contains(index) }
func (m *mockPeer) SetOurChoking(v bool)          { m.ourChoking.Store(v) }
func (m *mockPeer) SwapOurChoking(oldVal, newVal bool) bool {
	return m.ourChoking.CompareAndSwap(oldVal, newVal)
}
func (m *mockPeer) SetOurInterested(v bool) { m.ourInterested.Store(v) }
func (m *mockPeer) SwapOurInterested(oldVal, newVal bool) bool {
	return m.ourInterested.CompareAndSwap(oldVal, newVal)
}

// ── Timing ──────────────────────────────────────────────────────────

func (m *mockPeer) ConnectedAt() time.Time   { return m.connectedAt }
func (m *mockPeer) LastUnchokeAt() int64     { return m.lastUnchokeAt.Load() }
func (m *mockPeer) SetLastUnchokeAt(t int64) { m.lastUnchokeAt.Store(t) }

// ── Rates ───────────────────────────────────────────────────────────

func (m *mockPeer) DownloadRate() int64          { return m.downloadRate.Status().CurRate }
func (m *mockPeer) UploadRate() int64            { return m.uploadRate.Status().CurRate }
func (m *mockPeer) DownloadTotal() int64         { return m.downloadTotal }
func (m *mockPeer) UpdateDownloadRate(bytes int) { m.downloadRate.Update(bytes) }
func (m *mockPeer) UpdateUploadRate(bytes int)   { m.uploadRate.Update(bytes) }

// ── Request queue (download side) ───────────────────────────────────

func (m *mockPeer) OutstandingRequests() int {
	if m.resChan != nil {
		// Async mode: we can't track in-flight requests since responses
		// arrive through resChan. Let desiredSize throttle the queue.
		return 0
	}
	return int(m.outstanding)
}
func (m *mockPeer) QueueLen() int {
	return len(m.queued)
}
func (m *mockPeer) IsInQueue(chunk proto.ChunkRequest) bool {
	return m.inQueueMap[chunk]
}
func (m *mockPeer) EnqueueBlock(pieceIndex uint32, blockIndex int) {
	m.mu.Lock()
	m.enqueuedBlocks = append(m.enqueuedBlocks, PieceBlock{PieceIndex: pieceIndex, BlockIndex: blockIndex})
	// Don't enqueue if already closed — matches production where Close()
	// destroys the request queue via conn.Close().
	if !m.closed.Load() {
		m.queued = append(m.queued, PieceBlock{PieceIndex: pieceIndex, BlockIndex: blockIndex})
	}
	m.mu.Unlock()
}
func (m *mockPeer) SendBlockRequests() {
	m.sendBlockCalled++
	m.mu.Lock()
	queued := m.queued
	m.queued = m.queued[:0]
	m.mu.Unlock()
	for _, b := range queued {
		m.Request(pieceChunk(m.info, b.PieceIndex, b.BlockIndex))
	}
}
func (m *mockPeer) Request(chunk proto.ChunkRequest) {
	m.requestsSent = append(m.requestsSent, chunk)
	m.atomicSetInt32(&m.outstanding, m.outstanding+1)
	// Async delivery via resChan.
	if m.resChan != nil {
		go func() {
			m.resChan <- chunkSubmit{
				res: &proto.ChunkResponse{
					PieceIndex: chunk.PieceIndex,
					Begin:      chunk.Begin,
					Data:       make([]byte, chunk.Length),
				},
				peerID: m.peerID,
			}
		}()
	}
}
func (m *mockPeer) DesiredQueueSize() int { return int(m.desiredSize) }

// ── Picker integration ──────────────────────────────────────────────

func (m *mockPeer) LastPickResult() PickResult {
	return m.lastPickRes
}
func (m *mockPeer) SetLastPickResult(r PickResult) {
	m.lastPickRes = r
}
func (m *mockPeer) LastPickDebug() string     { return m.lastPickDebug }
func (m *mockPeer) SetLastPickDebug(s string) { m.lastPickDebug = s }

// requestABlock implements the scheduling logic for mock peers in tests.
func (m *mockPeer) requestABlock() {
	m.reqMu.Lock()
	defer m.reqMu.Unlock()

	d := m.dl
	if d == nil || m.closed.Load() || !d.IsActiveDownloading() {
		return
	}

	pickResult := d.picker.Load().RequestABlock(
		m.LastPickResult(),
		m.DesiredQueueSize(),
		m.OutstandingRequests(),
		m.QueueLen(),
		m.IsChoking(),
		m.PeerBitmap(),
		m.FastBitmap(),
		m.blockedPieces,
		m.onParole.Load(),
		m.peerID,
	)
	m.SetLastPickResult(pickResult)
	free := pickResult.FreeBlocks
	busy := pickResult.BusyBlocks

	if len(free) == 0 && len(busy) == 0 {
		return
	}

	for _, fb := range free {
		if m.IsInQueue(pieceChunk(d.info, fb.PieceIndex, fb.BlockIndex)) {
			continue
		}
		if !d.picker.Load().TryMarkAsRequesting(fb.PieceIndex, fb.BlockIndex, false) {
			continue
		}
		m.EnqueueBlock(fb.PieceIndex, fb.BlockIndex)
		if m.closed.Load() {
			d.picker.Load().AbortDownload(fb.PieceIndex, fb.BlockIndex)
		}
	}
	m.SendBlockRequests()

	// Busy blocks (endgame): only when no free blocks available, at most one.
	if len(free) == 0 {
		for _, bb := range busy {
			if m.IsInQueue(pieceChunk(d.info, bb.PieceIndex, bb.BlockIndex)) {
				continue
			}
			if !d.picker.Load().TryMarkAsRequesting(bb.PieceIndex, bb.BlockIndex, true) {
				continue
			}
			m.EnqueueBlock(bb.PieceIndex, bb.BlockIndex)
			if m.closed.Load() {
				d.picker.Load().AbortDownload(bb.PieceIndex, bb.BlockIndex)
				continue
			}
			m.SendBlockRequests()
			return
		}
	}
}

// ── Peer requests (upload side) ─────────────────────────────────────

func (m *mockPeer) PeerRequestCount() int { return len(m.peerRequests) }
func (m *mockPeer) ForEachPeerRequest(fn func(proto.ChunkRequest, empty.Empty) bool) {
	for req := range m.peerRequests {
		fn(req, empty.Empty{})
	}
}
func (m *mockPeer) DeletePeerRequest(req proto.ChunkRequest) { delete(m.peerRequests, req) }
func (m *mockPeer) PeerRequestExists(req proto.ChunkRequest) bool {
	_, ok := m.peerRequests[req]
	return ok
}
func (m *mockPeer) Response(res *proto.ChunkResponse) bool {
	if m.responseFunc != nil {
		return m.responseFunc(res)
	}
	return false
}

// ── Message sending ─────────────────────────────────────────────────

func (m *mockPeer) SendChoke()        {}
func (m *mockPeer) SendUnchoke()      {}
func (m *mockPeer) Have(index uint32) {}

// ── Transfer tracking ───────────────────────────────────────────────

func (m *mockPeer) HadTransfer() bool { return m.hadTrans }

// ── Read-only metadata ──────────────────────────────────────────────

func (m *mockPeer) Encrypted() bool     { return m.encrypted }
func (m *mockPeer) DhtEnabled() bool    { return m.dhtEnabled }
func (m *mockPeer) FastExtension() bool { return m.fastExtension }
func (m *mockPeer) SubExtensions() bool { return m.subExtension }

// ── Debug / info ────────────────────────────────────────────────────

func (m *mockPeer) PeerIDString() string { return m.peerIDString }
func (m *mockPeer) UserAgent() string    { return m.userAgent }
func (m *mockPeer) QueueLimit() uint32   { return m.queueLimit }

// ── Hash-fail punishment ────────────────────────────────────────────

func (m *mockPeer) OnHashFailed(pieceIndex uint32) {
	m.blockedPieces.Set(pieceIndex)
	m.mu.Lock()
	m.blockedPieceTimestamps[pieceIndex] = time.Now()
	m.mu.Unlock()
}
func (m *mockPeer) OnHashPassed(pieceIndex uint32) {
	m.blockedPieces.Unset(pieceIndex)
	m.suspectPieces.Unset(pieceIndex)
	m.mu.Lock()
	delete(m.blockedPieceTimestamps, pieceIndex)
	m.mu.Unlock()
}
func (m *mockPeer) IsBlocked(pieceIndex uint32) bool {
	return m.blockedPieces.Contains(pieceIndex)
}
func (m *mockPeer) IsBadPiece(pieceIndex uint32) bool {
	return m.suspectPieces.Contains(pieceIndex)
}
func (m *mockPeer) SetBadPiece(pieceIndex uint32) {
	m.suspectPieces.Set(pieceIndex)
}
func (m *mockPeer) BlockedPieceTime(pieceIndex uint32) (time.Time, bool) {
	m.mu.Lock()
	t, ok := m.blockedPieceTimestamps[pieceIndex]
	m.mu.Unlock()
	return t, ok
}

// ── Parole mode ──────────────────────────────────────────────────────────

func (m *mockPeer) SetOnParole(v bool) { m.onParole.Store(v) }
func (m *mockPeer) OnParole() bool     { return m.onParole.Load() }
func (m *mockPeer) BlockedCount() int  { return m.blockedPieces.Count() }
func (m *mockPeer) TrustPoints() int32 { return m.trustPoints.Load() }
func (m *mockPeer) AddTrustPoints(delta int32) int32 {
	for {
		old := m.trustPoints.Load()
		newVal := min(max(old+delta, -7), 8)
		if m.trustPoints.CompareAndSwap(old, newVal) {
			return newVal
		}
	}
}
func (m *mockPeer) IncHashFails() int32 {
	m.hashFails++
	return m.hashFails
}
