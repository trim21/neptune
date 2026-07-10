// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"net/netip"
	"time"

	"github.com/samber/lo"

	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/proto"
)

// ── Identity ─────────────────────────────────────────────────────────────

func (p *peerImpl) ID() uint64           { return p.id }
func (p *peerImpl) Addr() netip.AddrPort { return p.Address }
func (p *peerImpl) Incoming() bool       { return p.incoming }

// ── Lifecycle ────────────────────────────────────────────────────────────

func (p *peerImpl) Closed() bool          { return p.closed.Load() }
func (p *peerImpl) IsDisconnecting() bool { return p.disconnecting.Load() }
func (p *peerImpl) CloseError() error     { return p.closeErr }

// ── Piece availability ───────────────────────────────────────────────────

func (p *peerImpl) PeerBitmap() *bm.Bitmap { return p.Bitmap }
func (p *peerImpl) FastBitmap() *bm.Bitmap { return p.allowFast }
func (p *peerImpl) IsSeed() bool           { return p.isSeed.Load() }
func (p *peerImpl) PieceCount() uint32     { return p.Bitmap.Count() }

// ── Choke / interest state ───────────────────────────────────────────────

func (p *peerImpl) IsChoking() bool               { return p.peerChoking.Load() }
func (p *peerImpl) IsOurChoking() bool            { return p.ourChoking.Load() }
func (p *peerImpl) IsPeerInterested() bool        { return p.peerInterested.Load() }
func (p *peerImpl) IsOurInterested() bool         { return p.ourInterested.Load() }
func (p *peerImpl) IsSnubbed() bool               { return p.snubbed.Load() }
func (p *peerImpl) IsPreferred() bool             { return p.preferred.Load() }
func (p *peerImpl) AllowedFast(index uint32) bool { return p.allowFast.Contains(index) }

func (p *peerImpl) SetOurChoking(v bool) { p.ourChoking.Store(v) }
func (p *peerImpl) SwapOurChoking(oldVal, newVal bool) bool {
	return p.ourChoking.CompareAndSwap(oldVal, newVal)
}
func (p *peerImpl) SetOurInterested(v bool) { p.ourInterested.Store(v) }
func (p *peerImpl) SwapOurInterested(oldVal, newVal bool) bool {
	return p.ourInterested.CompareAndSwap(oldVal, newVal)
}

// ── Timing ───────────────────────────────────────────────────────────────

func (p *peerImpl) LastUnchokeAt() time.Time     { return p.lastUnchokeAt.Load() }
func (p *peerImpl) SetLastUnchokeAt(t time.Time) { p.lastUnchokeAt.Store(t) }

// ── Rates ────────────────────────────────────────────────────────────────

func (p *peerImpl) DownloadRate() int64          { return p.pieceDownloadRate.Status().CurRate }
func (p *peerImpl) UploadRate() int64            { return p.pieceUploadRate.Status().CurRate }
func (p *peerImpl) DownloadTotal() int64         { return p.pieceDownloadRate.Status().Total }
func (p *peerImpl) UpdateDownloadRate(bytes int) { p.pieceDownloadRate.Update(bytes) }
func (p *peerImpl) UpdateUploadRate(bytes int)   { p.pieceUploadRate.Update(bytes) }

// ── Request queue (download side) ────────────────────────────────────────

func (p *peerImpl) OutstandingRequests() int { return p.myRequests.Size() }
func (p *peerImpl) QueueLen() int            { return p.requestQueueLen() }

func (p *peerImpl) EnqueueBlock(pieceIndex uint32, blockIndex int) {
	p.rqMu.Lock()
	p.requestQueue = append(p.requestQueue, PieceBlock{PieceIndex: pieceIndex, BlockIndex: blockIndex})
	p.rqMu.Unlock()
}

func (p *peerImpl) SendBlockRequests()    { p.sendBlockRequests() }
func (p *peerImpl) DesiredQueueSize() int { return p.updateDesiredQueueSize() }

// ── Picker integration ───────────────────────────────────────────────────

func (p *peerImpl) LastPickResult() PickResult {
	p.lastPickResultMu.Lock()
	defer p.lastPickResultMu.Unlock()
	return p.lastPickResult
}

func (p *peerImpl) SetLastPickResult(r PickResult) {
	p.lastPickResultMu.Lock()
	defer p.lastPickResultMu.Unlock()
	p.lastPickResult = r
}

func (p *peerImpl) LastPickDebug() string {
	if s := p.lastPickDebug.Load(); s != nil {
		return *s
	}
	return "-"
}

func (p *peerImpl) SetLastPickDebug(s string) { p.lastPickDebug.Store(&s) }

// ── Peer requests (upload side) ──────────────────────────────────────────

func (p *peerImpl) PeerRequestCount() int { return p.peerRequests.Size() }
func (p *peerImpl) ForEachPeerRequest(fn func(proto.ChunkRequest, empty.Empty) bool) {
	p.peerRequests.Range(fn)
}
func (p *peerImpl) DeletePeerRequest(req proto.ChunkRequest) { p.peerRequests.Delete(req) }
func (p *peerImpl) PeerRequestExists(req proto.ChunkRequest) bool {
	_, ok := p.peerRequests.Load(req)
	return ok
}

// ── Message sending ──────────────────────────────────────────────────────

func (p *peerImpl) SendChoke()   { p.sendEventX(Event{Event: proto.Choke}) }
func (p *peerImpl) SendUnchoke() { p.sendEventX(Event{Event: proto.Unchoke}) }

// ── Transfer tracking ────────────────────────────────────────────────────

func (p *peerImpl) HadTransfer() bool { return p.hadTransfer }

// ── Read-only metadata ───────────────────────────────────────────────────

func (p *peerImpl) Encrypted() bool     { return p.encrypted }
func (p *peerImpl) DhtEnabled() bool    { return p.dhtEnabled }
func (p *peerImpl) FastExtension() bool { return p.fastExtension }
func (p *peerImpl) SubExtensions() bool { return p.subExtensions }

// ── Debug / info ─────────────────────────────────────────────────────────

func (p *peerImpl) PeerIDString() string { return p.peerID.Load().AsString() }
func (p *peerImpl) UserAgent() string    { return lo.FromPtrOr(p.userAgent.Load(), "") }
func (p *peerImpl) QueueLimit() uint32   { return p.queueLimit.Load() }

// ── Hash-fail punishment ────────────────────────────────────────────────

const maxHashFails = 3

// OnHashFailed increments the consecutive hash-fail counter for this peer.
// If the counter exceeds maxHashFails, the peer is disconnected and marked
// as failed so peerList increments failcount (preventing immediate reconnect).
func (p *peerImpl) OnHashFailed(pieceIndex uint32) {
	if !p.contributedPieces.Contains(pieceIndex) {
		return
	}
	fails := p.hashFails.Add(1)
	if fails >= maxHashFails {
		p.closeErr = ErrPeerSendInvalidData
		p.log.Warn().Uint32("hash_fails", fails).Msg("disconnecting peer: too many hash failures")
		p.Close()
	}
}

// OnHashPassed resets the hash-fail counter when this peer successfully
// delivers data for a piece that passes hash verification.
func (p *peerImpl) OnHashPassed(pieceIndex uint32) {
	if !p.contributedPieces.Contains(pieceIndex) {
		return
	}
	p.hashFails.Store(0)
}
