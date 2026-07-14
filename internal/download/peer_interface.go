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
	ConnectedAt() time.Time
	LastUnchokeAt() int64
	SetLastUnchokeAt(t int64)

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

	// ── Parole mode (trust-based corruption tracking) ─────────────────
	SetOnParole(v bool)
	// TrustPoints returns the peer's trust score, range [-7, 8].
	// A peer with <= -7 is banned.
	TrustPoints() int32
	// AddTrustPoints adjusts the trust score and returns the new value.
	AddTrustPoints(delta int32) int32
	// IncHashFails increments the hash-fail counter and returns the new value.
	IncHashFails() int32

	// ── Piece-level blocklist ─────────────────────────────────────────
	// IsBlocked returns true when this piece should not be requested from
	// this peer. This covers all reasons: the peer contributed to a
	// hash-failed piece, was confirmed as a bad source for this piece, etc.
	// (Whether the peer actually has the piece is separately checked via
	// PeerBitmap in the picker.)
	IsBlocked(pieceIndex uint32) bool
	// IsBadPiece returns true when this peer is confirmed to have corrupt
	// data for this piece (sole contributor on a hash-failed piece).
	IsBadPiece(pieceIndex uint32) bool
	SetBadPiece(pieceIndex uint32)
	// BlockedPieceTime returns when this piece was last blocked for this
	// peer (for gradual-unblock cycling).
	BlockedPieceTime(pieceIndex uint32) (time.Time, bool)
}
