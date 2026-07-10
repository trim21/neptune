// Copyright 2025 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"net/netip"
	"time"

	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/empty"
	"neptune/internal/proto"
)

// PeerInterface defines the management contract for a BitTorrent peer connection.
// Download uses this to manage peers (close, have, upload, stats, debug).
// Scheduling methods (requestABlock, sendBlockRequests, etc.) are NOT on this
// interface — they live on *peerImpl and are accessed via the private scheduler
// interface where needed.
//
// In dev builds (!release), Peer is this interface, enabling mock-based testing.
// In release builds, Peer is *peerImpl — a concrete pointer with zero dispatch overhead.
type PeerInterface interface {
	// ── Identity ─────────────────────────────────────────────────────
	ID() uint64
	Addr() netip.AddrPort
	Incoming() bool

	// ── Lifecycle ────────────────────────────────────────────────────
	Close()
	Closed() bool
	IsDisconnecting() bool
	CloseError() error

	// ── Piece availability ───────────────────────────────────────────
	PeerBitmap() *bm.Bitmap
	FastBitmap() *bm.Bitmap
	IsSeed() bool
	PieceCount() uint32

	// ── Choke / interest state ───────────────────────────────────────
	IsChoking() bool
	IsOurChoking() bool
	IsPeerInterested() bool
	IsOurInterested() bool
	IsSnubbed() bool
	IsPreferred() bool
	AllowedFast(index uint32) bool
	SetOurChoking(v bool)
	SwapOurChoking(oldVal, newVal bool) bool
	SetOurInterested(v bool)
	SwapOurInterested(oldVal, newVal bool) bool

	// ── Timing ───────────────────────────────────────────────────────
	LastUnchokeAt() time.Time
	SetLastUnchokeAt(t time.Time)

	// ── Rates ────────────────────────────────────────────────────────
	DownloadRate() int64
	UploadRate() int64
	DownloadTotal() int64
	UpdateDownloadRate(bytes int)
	UpdateUploadRate(bytes int)

	// ── Peer requests (upload side) ──────────────────────────────────
	PeerRequestCount() int
	ForEachPeerRequest(fn func(proto.ChunkRequest, empty.Empty) bool)
	DeletePeerRequest(req proto.ChunkRequest)
	PeerRequestExists(req proto.ChunkRequest) bool
	Response(res *proto.ChunkResponse) bool

	// ── Message sending ──────────────────────────────────────────────
	SendChoke()
	SendUnchoke()
	Have(index uint32)

	// ── Transfer tracking ────────────────────────────────────────────
	HadTransfer() bool

	// ── Read-only metadata (set once during handshake) ────────────────
	Encrypted() bool
	DhtEnabled() bool
	FastExtension() bool
	SubExtensions() bool

	// ── Debug / info ──────────────────────────────────────────────────
	PeerIDString() string
	UserAgent() string
	QueueLimit() uint32

	// ── Hash-fail punishment ──────────────────────────────────────────
	OnHashFailed(pieceIndex uint32)
	OnHashPassed(pieceIndex uint32)

	// ── Blocked pieces (per-peer corrupt piece blocklist) ─────────────
	BlockedPieces() *bm.Bitmap
}
