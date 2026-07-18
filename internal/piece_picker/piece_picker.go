// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package piece_picker

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/atomic"

	"neptune/internal/meta"
	"neptune/internal/pkg/assert"
	"neptune/internal/pkg/bm"
)

// PiecePickStrategy controls the piece selection order.
type PiecePickStrategy uint32

const (
	// StrategyRarestFirst prioritizes rare pieces (default).
	StrategyRarestFirst PiecePickStrategy = 0
	// StrategySequential picks pieces in ascending index order.
	StrategySequential PiecePickStrategy = 1
)

// RequestGate provides a read-only view of an owner's atomic state. A picker
// may create claims only while the state equals enabledValue.
type RequestGate struct {
	state        *atomic.Uint32
	enabledValue uint32
}

// NewRequestGate creates a read-only admission gate backed by state.
func NewRequestGate(state *atomic.Uint32, enabledValue uint32) RequestGate {
	if state == nil {
		panic("piece picker request gate requires a state")
	}
	return RequestGate{state: state, enabledValue: enabledValue}
}

func (g RequestGate) enabled() bool {
	return g.state.Load() == g.enabledValue
}

// String returns the human-readable name of the strategy.
func (s PiecePickStrategy) String() string {
	switch s {
	case StrategySequential:
		return "sequential"
	case StrategyRarestFirst:
		return "rarest-first"
	default:
		return "<invalid>"
	}
}

// PiecePickStrategyFromString parses a strategy string.
func PiecePickStrategyFromString(s string) (PiecePickStrategy, error) {
	switch s {
	case "rarest-first":
		return StrategyRarestFirst, nil
	case "sequential":
		return StrategySequential, nil
	default:
		return 0, fmt.Errorf("invalid piece pick strategy %q: must be 'rarest-first' or 'sequential'", s)
	}
}

// blockState represents the state of an individual block within a piece.
type blockState uint8

const (
	blockStateNone      blockState = iota // block is free, not yet requested
	blockStateRequested                   // block is currently requested from a peer
	blockStateResponded                   // block data received from peer
)

// blockStates stores per-block state using 2 bits per block packed into bytes.
// 00=None, 01=Requested, 10=Writing, 11=Finished. 4 blocks per byte.
type blockStates struct {
	data []byte
}

func newBlockStates(numBlocks int) blockStates {
	return blockStates{data: make([]byte, (numBlocks+3)/4)}
}

func (bs blockStates) get(idx int) blockState {
	return blockState((bs.data[idx>>2] >> ((idx & 3) << 1)) & 0x3)
}

// countNone counts blockStateNone blocks in [startIdx, startIdx+count).
// Reads one byte per 4 blocks for the bulk, avoiding repeated data[idx>>2] loads.
//
//nolint:dupl
func (bs blockStates) countNone(startIdx, count int) int {
	n := 0
	// unaligned prefix: step one block at a time until aligned
	for count > 0 && startIdx&3 != 0 {
		if bs.get(startIdx) == blockStateNone {
			n++
		}
		startIdx++
		count--
	}
	if count == 0 {
		return n
	}
	// aligned bulk: 4 blocks per byte
	byteIdx := startIdx >> 2
	fullBytes := count >> 2
	for i := range fullBytes {
		b := bs.data[byteIdx+i]
		// fast path: all zero means all 4 blocks are None
		if b == 0 {
			n += 4
			continue
		}
		if b&0x3 == 0 {
			n++
		}
		if (b>>2)&0x3 == 0 {
			n++
		}
		if (b>>4)&0x3 == 0 {
			n++
		}
		if (b>>6)&0x3 == 0 {
			n++
		}
	}
	// unaligned suffix
	for i := fullBytes * 4; i < count; i++ {
		if bs.get(startIdx+i) == blockStateNone {
			n++
		}
	}
	return n
}

// countRequested counts blockStateRequested blocks in [startIdx, startIdx+count).
//
//nolint:dupl
func (bs blockStates) countRequested(startIdx, count int) int {
	n := 0
	for count > 0 && startIdx&3 != 0 {
		if bs.get(startIdx) == blockStateRequested {
			n++
		}
		startIdx++
		count--
	}
	if count == 0 {
		return n
	}
	byteIdx := startIdx >> 2
	fullBytes := count >> 2
	for i := range fullBytes {
		b := bs.data[byteIdx+i]
		// fast path: all Requested is 0b01010101 = 0x55
		if b == 0x55 {
			n += 4
			continue
		}
		if b&0x3 == 1 {
			n++
		}
		if (b>>2)&0x3 == 1 {
			n++
		}
		if (b>>4)&0x3 == 1 {
			n++
		}
		if (b>>6)&0x3 == 1 {
			n++
		}
	}
	for i := fullBytes * 4; i < count; i++ {
		if bs.get(startIdx+i) == blockStateRequested {
			n++
		}
	}
	return n
}

func (bs blockStates) set(idx int, s blockState) {
	shift := (idx & 3) << 1
	bs.data[idx>>2] = (bs.data[idx>>2] &^ (0x3 << shift)) | (byte(s) << shift)
}

func (bs blockStates) resetAll() {
	clear(bs.data)
}

// downloadingPiece represents a piece that is partially downloaded.
type downloadingPiece struct {
	infoIdx         int
	index           uint32
	blocksInPiece   uint16
	responded       uint16 // blocks received from peer
	requested       uint16
	passedHashCheck bool
	locked          bool
}

// piecePriority computes a score for a piece.
// Higher score = more urgent. Mirrors libtorrent: (availability + 1) * priority_factor.
const priorityFactor = 3

// maxBlockRetries caps how many extra peers may concurrently request a block
// that is already requested by another peer. In endgame, this bounds waste_dupe
// while still providing enough redundancy for tail-block completion.
const maxBlockRetries = 2

const maxClaimsPerBlock = 1 + maxBlockRetries

const diagnosticInterval = time.Minute

// PieceBlock identifies a block within a piece.
type PieceBlock struct {
	PieceIndex uint32
	BlockIndex uint32
}

// BlockClaim is an opaque, peer-owned claim on a block. The token is kept
// private so callers can only pass claims returned by PickAndClaim.
type BlockClaim struct {
	Block PieceBlock
	token uint64
}

type claimRecord struct {
	block PieceBlock
	owner uint64
}

type blockClaims struct {
	tokens [maxClaimsPerBlock]uint64
	count  uint8
}

// PickRequest describes one atomic pick-and-claim operation.
type PickRequest struct {
	Bitfield         *bm.LockFreeBitmap
	AllowedFast      *bm.LockFreeBitmap
	BlockedPieces    *bm.LockFreeBitmap
	SuggestedPieces  []uint32
	PeerID           uint64
	NumBlocks        int
	PreferContiguous int
	Choked           bool
	OnParole         bool
}

type partialInfo struct {
	blocksInPiece int
	ratio         float64
	pieceIndex    uint32
	priority      uint32
}

// PiecePicker is a global block-level piece picker, mirroring libtorrent's
// piece_picker. It centralizes tracking of which blocks are requested/writing/
// finished, and provides block-level selection for peers.
//
// All public methods are safe for concurrent use.
type PiecePicker struct {
	lastDiagAt             time.Time
	missingBm              *bm.LockFreeBitmap
	emptyPiecesBm          *bm.LockFreeBitmap
	parolePieceOwners      map[uint32]uint64
	activeClaims           map[uint64]claimRecord
	claimsByBlock          map[uint32]blockClaims
	claimsByPeer           map[uint64]map[uint64]struct{}
	strategy               *atomic.Uint32
	chunkDoneBm            *bm.Bitmap
	requestGate            RequestGate
	pickScratch            PickResult
	partials               []partialInfo
	downloadingPieces      []downloadingPiece
	blockInfos             blockStates
	respondedBlocks        []uint16
	pieces                 []uint32
	availability           []uint16
	piecePriorities        []uint32
	info                   meta.Info
	diagSkippedDownloading int
	downloadQueueSize      int
	staleAccepts           uint64
	diagNumWant            int
	diagSkippedResponded   int
	diagSkippedBitfield    int
	diagSkippedChoked      int
	diagSkippedBlocked     int
	freeBlocks             int
	diagFreeBlocks         int
	staleReleases          uint64
	numWantLeft            int
	nextClaimToken         uint64
	blockSize              int64
	mu                     sync.Mutex
	blocksPerPiece         uint32
	numPieces              uint32
	numCompletedPieces     uint32
	diagDirty              bool
	dirty                  bool
	partialsDirty          bool
}

// NewPiecePicker creates a new piece picker for the given torrent info.
// missingBm is the Download's missing bitmap (wantedBm & ~completedBm) — the
// picker uses it for lock-free reads in the hot path.
func NewPiecePicker(
	info meta.Info,
	missingBm *bm.LockFreeBitmap,
	chunkDoneBm *bm.Bitmap,
	strategy *atomic.Uint32,
	requestGate RequestGate,
) *PiecePicker {
	if requestGate.state == nil {
		panic("piece picker requires a request gate")
	}
	numPieces := info.NumPieces
	blocksPerPiece := uint32((info.PieceLength + meta.DefaultBlockSize - 1) / meta.DefaultBlockSize)

	// Total blocks across all pieces; we start with everything free.
	fullPieceBlocks := int(blocksPerPiece) * (int(numPieces) - 1)
	lastPieceBlocks := int((info.LastPieceSize + meta.DefaultBlockSize - 1) / meta.DefaultBlockSize)
	freeBlocks := fullPieceBlocks + lastPieceBlocks

	pp := &PiecePicker{
		info:              info,
		numPieces:         numPieces,
		blocksPerPiece:    blocksPerPiece,
		freeBlocks:        freeBlocks,
		blockSize:         meta.DefaultBlockSize,
		availability:      make([]uint16, numPieces),
		piecePriorities:   make([]uint32, numPieces),
		pieces:            make([]uint32, numPieces),
		parolePieceOwners: make(map[uint32]uint64),
		activeClaims:      make(map[uint64]claimRecord),
		claimsByBlock:     make(map[uint32]blockClaims),
		claimsByPeer:      make(map[uint64]map[uint64]struct{}),
		missingBm:         missingBm,
		emptyPiecesBm:     bm.NewLockFreeBitmap(numPieces),
		chunkDoneBm:       chunkDoneBm,
		strategy:          strategy,
		requestGate:       requestGate,
		dirty:             true,
		partialsDirty:     true,
		blockInfos:        newBlockStates(int(numPieces) * int(blocksPerPiece)),
		respondedBlocks:   make([]uint16, numPieces),
	}

	// initialize pieces array
	for i := range numPieces {
		pp.pieces[i] = i
		pp.piecePriorities[i] = 1 * priorityFactor // initial priority = (0+1)*3
	}

	// initialize block info array: set piece_index for each block
	// (for debugging only — removed in production build)

	return pp
}

// SetStrategy updates the piece selection strategy.
func (pp *PiecePicker) SetStrategy(s PiecePickStrategy) {
	if pp.strategy != nil {
		pp.strategy.Store(uint32(s))
	}
}

func (pp *PiecePicker) numBlocksInPiece(pieceIndex uint32) uint16 {
	pieceSize := pp.info.PieceLength
	if pieceIndex == pp.info.NumPieces-1 {
		pieceSize = pp.info.LastPieceSize
	}
	return uint16((pieceSize + meta.DefaultBlockSize - 1) / meta.DefaultBlockSize)
}

// blockInfoIdx returns the starting index in blockInfos for the given piece.
func (pp *PiecePicker) blockInfoIdx(pieceIndex uint32) int {
	return int(pieceIndex) * int(pp.blocksPerPiece)
}

// chunkIndex returns the global chunk index for a given piece and block.
func (pp *PiecePicker) chunkIndex(pieceIndex uint32, blockIndex int) uint32 {
	return pieceIndex*pp.blocksPerPiece + uint32(blockIndex)
}

// IncRefcount increments the reference count for all blocks in the given piece.
// Called when a peer acquires a piece (bitfield/have).
func (pp *PiecePicker) IncRefcount(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.availability[pieceIndex]++
	if pp.availability[pieceIndex] == 1 {
		// piece went from unavailable to available
		pp.dirty = true
	}
	if pp.findDownloadingPiece(pieceIndex) != nil {
		pp.partialsDirty = true
	}
}

// DecRefcount decrements the reference count for all blocks in the given piece.
// Called when a peer loses a piece (disconnect, dont_have).
func (pp *PiecePicker) DecRefcount(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if pp.availability[pieceIndex] > 0 {
		pp.availability[pieceIndex]--
	}
	if pp.availability[pieceIndex] == 0 {
		pp.dirty = true
	}
	if pp.findDownloadingPiece(pieceIndex) != nil {
		pp.partialsDirty = true
	}
}

// PieceIsAvailable returns true if pieceIndex has at least one peer with it.
func (pp *PiecePicker) PieceIsAvailable(pieceIndex uint32) bool {
	if pp == nil {
		return false
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.availability[pieceIndex] > 0
}

// IsPieceInCandidates returns true if the piece is in the picker's candidate list.
func (pp *PiecePicker) IsPieceInCandidates(pieceIndex uint32) bool {
	if pp == nil {
		return false
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return slices.Contains(pp.pieces, pieceIndex)
}

// WeHave marks a piece as completed (we now have it).
// It clears all block states for that piece and increments the completed counter.
func (pp *PiecePicker) WeHave(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(pieceIndex)
	pp.invalidatePieceClaimsUnsafe(pieceIndex)
	for i := range int(nb) {
		oldState := pp.blockInfos.get(idx + i)
		switch oldState {
		case blockStateRequested:
			pp.downloadQueueSize--
		case blockStateNone:
			pp.freeBlocks--
		}
		pp.blockInfos.set(idx+i, blockStateResponded)
	}
	pp.respondedBlocks[pieceIndex] = nb
	pp.removeDownloadingPieceUnsafe(pieceIndex)
	pp.numCompletedPieces++
	delete(pp.parolePieceOwners, pieceIndex)

	pp.dirty = true
}

// ReleaseAllClaims releases every claim active while it holds the picker lock.
// A racing PickAndClaim may create claims afterward; its caller still owns and
// must consume every returned token.
func (pp *PiecePicker) ReleaseAllClaims() int {
	if pp == nil {
		return 0
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	released := len(pp.activeClaims)
	for token, record := range pp.activeClaims {
		pp.releaseClaimUnsafe(BlockClaim{Block: record.block, token: token})
	}
	clear(pp.parolePieceOwners)
	return released
}

// AcceptResponse consumes a live claim and marks its block responded. All
// sibling endgame claims are invalidated atomically.
func (pp *PiecePicker) AcceptResponse(claim BlockClaim) bool {
	if pp == nil {
		return false
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	record, ok := pp.activeClaims[claim.token]
	if !ok || record.block != claim.Block {
		pp.staleAccepts++
		return false
	}
	idx := pp.blockInfoIdx(claim.Block.PieceIndex) + int(claim.Block.BlockIndex)
	assert.Equal(pp.blockInfos.get(idx), blockStateRequested, "active claim on non-requested block")
	if pp.blockInfos.get(idx) != blockStateRequested {
		pp.staleAccepts++
		return false
	}

	chunkIdx := pp.chunkIndex(claim.Block.PieceIndex, int(claim.Block.BlockIndex))
	claims := pp.claimsByBlock[chunkIdx]
	assert.NotEqual(claims.count, uint8(0), "active claim missing block index")
	for i := range int(claims.count) {
		token := claims.tokens[i]
		pp.deleteClaimIndexesUnsafe(token, pp.activeClaims[token])
	}

	pp.blockInfos.set(idx, blockStateResponded)
	pp.respondedBlocks[claim.Block.PieceIndex]++
	pp.downloadQueueSize--
	if dp := pp.findDownloadingPiece(claim.Block.PieceIndex); dp != nil {
		if dp.requested > 0 {
			dp.requested--
		}
		dp.responded++
		pp.partialsDirty = true
	}
	return true
}

// ReleaseClaim releases exactly one claim. It is safe to call for a stale
// claim; false means another terminal path already consumed it.
func (pp *PiecePicker) ReleaseClaim(claim BlockClaim) bool {
	if pp == nil {
		return false
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if !pp.releaseClaimUnsafe(claim) {
		pp.staleReleases++
		return false
	}
	return true
}

// ReleasePeerClaims releases all live claims owned by peerID and clears its
// parole piece ownerships.
func (pp *PiecePicker) ReleasePeerClaims(peerID uint64) int {
	if pp == nil {
		return 0
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	claims := pp.claimsByPeer[peerID]
	released := len(claims)
	for token := range claims {
		record, ok := pp.activeClaims[token]
		if ok {
			pp.releaseClaimUnsafe(BlockClaim{Block: record.block, token: token})
		}
	}
	delete(pp.claimsByPeer, peerID)
	for pieceIndex, owner := range pp.parolePieceOwners {
		if owner == peerID {
			delete(pp.parolePieceOwners, pieceIndex)
		}
	}
	return released
}

func (pp *PiecePicker) releaseClaimUnsafe(claim BlockClaim) bool {
	record, ok := pp.activeClaims[claim.token]
	if !ok || record.block != claim.Block {
		return false
	}

	remaining := pp.deleteClaimIndexesUnsafe(claim.token, record)
	if remaining != 0 {
		return true
	}

	pieceIndex := record.block.PieceIndex
	idx := pp.blockInfoIdx(pieceIndex) + int(record.block.BlockIndex)
	assert.Equal(pp.blockInfos.get(idx), blockStateRequested, "active claim on non-requested block")
	if pp.blockInfos.get(idx) != blockStateRequested {
		return true
	}
	pp.blockInfos.set(idx, blockStateNone)
	pp.downloadQueueSize--
	pp.freeBlocks++
	if dp := pp.findDownloadingPiece(pieceIndex); dp != nil {
		if dp.requested > 0 {
			dp.requested--
		}
		if dp.requested == 0 && dp.responded == 0 {
			pp.removeDownloadingPieceUnsafe(pieceIndex)
			delete(pp.parolePieceOwners, pieceIndex)
		}
	}
	return true
}

func (pp *PiecePicker) deleteClaimIndexesUnsafe(token uint64, record claimRecord) uint8 {
	delete(pp.activeClaims, token)
	if peerClaims := pp.claimsByPeer[record.owner]; peerClaims != nil {
		delete(peerClaims, token)
	}

	chunkIdx := pp.chunkIndex(record.block.PieceIndex, int(record.block.BlockIndex))
	claims := pp.claimsByBlock[chunkIdx]
	found := false
	for i := range int(claims.count) {
		if claims.tokens[i] != token {
			continue
		}
		last := int(claims.count) - 1
		claims.tokens[i] = claims.tokens[last]
		claims.tokens[last] = 0
		claims.count--
		found = true
		break
	}
	assert.Equal(found, true, "active claim missing token in block index")
	if claims.count == 0 {
		delete(pp.claimsByBlock, chunkIdx)
	} else {
		pp.claimsByBlock[chunkIdx] = claims
	}
	return claims.count
}

func (pp *PiecePicker) invalidatePieceClaimsUnsafe(pieceIndex uint32) {
	for blockIndex := range uint32(pp.numBlocksInPiece(pieceIndex)) {
		chunkIdx := pp.chunkIndex(pieceIndex, int(blockIndex))
		claims := pp.claimsByBlock[chunkIdx]
		for i := range int(claims.count) {
			token := claims.tokens[i]
			pp.deleteClaimIndexesUnsafe(token, pp.activeClaims[token])
		}
	}
}

// IsFinished returns true if the block is finished.
func (pp *PiecePicker) IsFinished(pieceIndex uint32, blockIndex uint32) bool {
	if pp == nil {
		return true
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex) + int(blockIndex)
	return pp.blockInfos.get(idx) == blockStateResponded
}

// rebuildPriorities re-sorts the piece priority array.
// Only fully-completed pieces (all blocks finished) are excluded.
// Partially downloaded pieces stay in the array — pickPieces handles them
// via the partial-pieces phase first, then falls back to the priority list.
func (pp *PiecePicker) rebuildPriorities(strategy PiecePickStrategy) {
	if pp == nil {
		return
	}
	if !pp.dirty {
		return
	}

	pp.pieces = pp.pieces[:0]
	// Filter to pieces that are wanted, not completed, and not awaiting hash check.
	for pi := range pp.numPieces {
		if !pp.missingBm.Contains(pi) || pp.allBlocksResponded(pi) {
			continue
		}
		pp.pieces = append(pp.pieces, pi)
	}

	if strategy == StrategySequential {
		// Sequential: pieces from ToArray() are already in ascending order.
	} else {
		// Rarest-first (default): sort by priority descending, then by piece index.
		slices.SortFunc(pp.pieces, func(a, b uint32) int {
			pa := pp.piecePriorities[a]
			pb := pp.piecePriorities[b]
			if pa != pb {
				if pa > pb {
					return -1
				}
				return 1
			}
			if a < b {
				return -1
			}
			return 1
		})
	}

	pp.numWantLeft = len(pp.pieces)
	pp.dirty = false
}

// allBlocksFinished returns true if every block of the given piece is finished.
func (pp *PiecePicker) allBlocksResponded(pieceIndex uint32) bool {
	if pp == nil {
		return true
	}
	return pp.respondedBlocks[pieceIndex] == pp.numBlocksInPiece(pieceIndex)
}

// updatePiecePriority recalculates the priority for a piece based on its availability.
func (pp *PiecePicker) updatePiecePriority(pieceIndex uint32) {
	if pp == nil {
		return
	}
	avail := pp.availability[pieceIndex]
	if avail == 0 {
		avail = 1 // ensure positive priority
	}
	pp.piecePriorities[pieceIndex] = uint32(avail+1) * priorityFactor
}

// PickResult holds the result of PickPieces.
type PickResult struct {
	// FreeBlocks are blocks not requested by any peer.
	FreeBlocks []PieceBlock
	// BusyBlocks are blocks already requested by another peer, for endgame fallback.
	BusyBlocks []PieceBlock
}

// PickPieces picks blocks for a peer using the given piece pick strategy.
//
// Parameters:
//   - bitfield: pieces the peer has
//   - choked: whether the peer has us choked (only allowed fast pieces)
//   - allowedFast: bitmap of allowed fast pieces
//   - numBlocks: desired number of blocks to pick
//   - preferContiguous: >0 means prefer whole pieces, value is the number of contiguous blocks
//   - suggestedPieces: pieces suggested by the peer
//   - strategy: piece selection strategy (rarest-first or sequential)
//
// It first prioritizes partial pieces (highest progress first), then uses the given strategy.
//

func (pp *PiecePicker) pickPiecesUnsafe(
	bitfield *bm.LockFreeBitmap,
	choked bool,
	allowedFast *bm.LockFreeBitmap,
	blockedPieces *bm.LockFreeBitmap,
	numBlocks int,
	preferContiguous int,
	suggestedPieces []uint32,
	onParole bool,
	peerID uint64,
	// re-use the slice to avoid alloc
	result PickResult,
) PickResult {
	strategy := StrategyRarestFirst
	if pp.strategy != nil {
		strategy = PiecePickStrategy(pp.strategy.Load())
	}

	pp.rebuildPriorities(strategy)
	// Enter endgame whenever no truly free (None) blocks remain, regardless
	// of how many pieces still appear open. This correctly handles orphaned
	// Requested blocks (from disconnected peers) that would otherwise prevent
	// endgame from activating.
	isEndgame := pp.freeBlocks == 0

	// Reuse slices from previous call.
	result.FreeBlocks = result.FreeBlocks[:0]
	result.BusyBlocks = result.BusyBlocks[:0]

	// ── Startup mode: no completed pieces and no partial pieces yet ──
	// Pick a random medium-rarity piece to quickly get something to upload.
	// Skip startup mode in sequential strategy — always start from piece 0.
	if strategy != StrategySequential && pp.numCompletedPieces == 0 && len(pp.downloadingPieces) == 0 {
		pp.pickStartupBlock(bitfield, choked, allowedFast, &result)
		if len(result.FreeBlocks) > 0 {
			return result
		}
	}

	// Phase 1: partial pieces (pieces in downloading state with some blocks finished).
	// Cache the sorted view, rebuild only when a downloading piece's ratio or priority changed.
	// Peer-specific filtering (bitfield, choke, parole, etc.) is applied during iteration
	// so that different peers can share the same sorted cache.
	if pp.partialsDirty {
		pp.partials = pp.partials[:0]

		for _, dp := range pp.downloadingPieces {
			if !pp.missingBm.Contains(dp.index) {
				continue
			}
			pp.partials = append(pp.partials, partialInfo{
				pieceIndex:    dp.index,
				blocksInPiece: int(dp.blocksInPiece),
				ratio:         float64(dp.responded) / float64(dp.blocksInPiece),
				priority:      pp.piecePriorities[dp.index],
			})
		}

		slices.SortFunc(pp.partials, func(a, b partialInfo) int {
			if a.ratio != b.ratio {
				if a.ratio > b.ratio {
					return -1
				}
				return 1
			}
			if a.priority != b.priority {
				if a.priority > b.priority {
					return -1
				}
				return 1
			}
			return 0
		})
		pp.partialsDirty = false
	}

	// Pick from partial pieces first — apply peer-specific filtering here.
	for _, p := range pp.partials {
		if numBlocks <= 0 {
			break
		}
		// missingBm is owned by Download and can change without invalidating
		// the cached partial ordering.
		if !pp.missingBm.Contains(p.pieceIndex) {
			continue
		}
		if !bitfield.Contains(p.pieceIndex) {
			continue
		}
		if choked && !allowedFast.Contains(p.pieceIndex) {
			continue
		}
		if blockedPieces.Contains(p.pieceIndex) {
			continue
		}
		dp := pp.findDownloadingPiece(p.pieceIndex)
		if dp == nil {
			continue
		}
		if onParole {
			owner, ok := pp.parolePieceOwners[p.pieceIndex]
			if ok && owner != peerID {
				continue // piece claimed by another peer
			}
		} else {
			// Non-parole peer joining a piece owned by someone else:
			// clear ownership so the original parole peer sees it as contested.
			owner, ok := pp.parolePieceOwners[p.pieceIndex]
			if ok && owner != peerID {
				delete(pp.parolePieceOwners, p.pieceIndex)
			}
		}

		idx := pp.blockInfoIdx(p.pieceIndex)
		for i := range p.blocksInPiece {
			switch pp.blockInfos.get(idx + i) {
			case blockStateNone:
				if preferContiguous <= 0 && numBlocks > 0 {
					result.FreeBlocks = append(result.FreeBlocks, PieceBlock{p.pieceIndex, uint32(i)})
					numBlocks--
				}
			case blockStateRequested:
				// busy block — only replicate in endgame, and skip already-retried blocks
				// to prevent cascading duplicate requests across many peers.
				if isEndgame && numBlocks > 0 {
					if pp.claimsByBlock[pp.chunkIndex(p.pieceIndex, i)].count < maxClaimsPerBlock {
						result.BusyBlocks = append(result.BusyBlocks, PieceBlock{p.pieceIndex, uint32(i)})
					}
				}
			}
		}
	}

	// Endgame fallback: when in endgame mode, collect busy blocks from
	// stalled downloading pieces that have 0 free blocks (e.g. orphaned
	// request blocks from disconnected peers).
	// Pieces with free>0 were already handled in the partials loop above.
	if numBlocks > 0 && isEndgame {
		for _, dp := range pp.downloadingPieces {
			if !pp.missingBm.Contains(dp.index) {
				continue
			}
			if !bitfield.Contains(dp.index) {
				continue
			}
			if choked && !allowedFast.Contains(dp.index) {
				continue
			}
			if blockedPieces.Contains(dp.index) {
				continue
			}
			idx := pp.blockInfoIdx(dp.index)
			if pp.blockInfos.countNone(idx, int(dp.blocksInPiece)) > 0 {
				continue // handled in partials loop
			}
			for i := range int(dp.blocksInPiece) {
				if pp.blockInfos.get(idx+i) == blockStateRequested {
					if pp.claimsByBlock[pp.chunkIndex(dp.index, i)].count < maxClaimsPerBlock {
						result.BusyBlocks = append(result.BusyBlocks, PieceBlock{dp.index, uint32(i)})
					}
				}
			}
		}
	}

	// Phase 2: suggested pieces
	for _, pi := range suggestedPieces {
		if numBlocks <= 0 {
			break
		}
		if !bitfield.Contains(pi) {
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			continue
		}
		if blockedPieces.Contains(pi) {
			continue
		}
		if onParole && pp.findDownloadingPiece(pi) != nil {
			continue // parole peers only pick unclaimed pieces
		}
		pp.pickBlocksFromPiece(pi, &numBlocks, &result)
		pp.parolePieceOwners[pi] = peerID
	}

	// Phase 3: rarest-first via cursor scan — single pass over pre-sorted pieces.
	// pp.pieces is already sorted by priority descending (rebuildPriorities),
	// so a single cursor walk finds the next eligible piece in O(n) total.
	for cursor := 0; numBlocks > 0 && cursor < len(pp.pieces); cursor++ {
		pi := pp.pieces[cursor]

		if !pp.missingBm.Contains(pi) {
			continue
		}
		if !bitfield.Contains(pi) {
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			continue
		}
		if blockedPieces.Contains(pi) {
			continue
		}
		if pp.allBlocksResponded(pi) {
			continue
		}
		if pp.isAlreadyPicked(pi, &result) {
			continue
		}
		if pp.findDownloadingPiece(pi) != nil {
			continue
		}

		pp.pickBlocksFromPiece(pi, &numBlocks, &result)
		pp.parolePieceOwners[pi] = peerID
	}

	// Diagnostic: record filter skip reasons when no blocks were picked.
	if numBlocks > 0 && len(result.FreeBlocks) == 0 && len(result.BusyBlocks) == 0 {
		pp.recordDiagIfDueUnsafe(bitfield, choked, allowedFast, blockedPieces, true)
	}

	return result
}

// recordDiag scans all candidate pieces and records why they were skipped.
// Call after a failed pick to surface which filter is blocking progress.
func (pp *PiecePicker) recordDiag(
	bitfield *bm.LockFreeBitmap,
	choked bool,
	allowedFast *bm.LockFreeBitmap,
	blockedPieces *bm.LockFreeBitmap,
	skipDownloading bool,
) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.lastDiagAt = time.Now()
	pp.recordDiagUnsafe(bitfield, choked, allowedFast, blockedPieces, skipDownloading)
}

func (pp *PiecePicker) recordDiagIfDueUnsafe(
	bitfield *bm.LockFreeBitmap,
	choked bool,
	allowedFast *bm.LockFreeBitmap,
	blockedPieces *bm.LockFreeBitmap,
	skipDownloading bool,
) {
	now := time.Now()
	if !pp.lastDiagAt.IsZero() && now.Before(pp.lastDiagAt.Add(diagnosticInterval)) {
		return
	}
	pp.lastDiagAt = now
	pp.recordDiagUnsafe(bitfield, choked, allowedFast, blockedPieces, skipDownloading)
}

// recordDiagUnsafe is the lock-free variant; caller must hold pp.mu.
func (pp *PiecePicker) recordDiagUnsafe(
	bitfield *bm.LockFreeBitmap,
	choked bool,
	allowedFast *bm.LockFreeBitmap,
	blockedPieces *bm.LockFreeBitmap,
	skipDownloading bool,
) {
	pp.diagDirty = pp.dirty
	pp.diagFreeBlocks = pp.freeBlocks
	pp.diagNumWant = len(pp.pieces)
	pp.diagSkippedResponded = 0
	pp.diagSkippedBitfield = 0
	pp.diagSkippedChoked = 0
	pp.diagSkippedBlocked = 0
	pp.diagSkippedDownloading = 0
	for _, pi := range pp.pieces {
		if !pp.missingBm.Contains(pi) || pp.allBlocksResponded(pi) {
			pp.diagSkippedResponded++
			continue
		}
		if !bitfield.Contains(pi) {
			pp.diagSkippedBitfield++
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			pp.diagSkippedChoked++
			continue
		}
		if blockedPieces.Contains(pi) {
			pp.diagSkippedBlocked++
			continue
		}
		if skipDownloading {
			if pp.findDownloadingPiece(pi) != nil {
				pp.diagSkippedDownloading++
				continue
			}
		}
	}
}

// pickStartupBlock implements the startup mode strategy:
// when we have zero completed pieces, pick a random piece of medium rarity
// (not too rare to avoid stalling, not too common to be worth trading later).
//
// Caller must hold pp.mu.
func (pp *PiecePicker) pickStartupBlock(
	bitfield *bm.LockFreeBitmap,
	choked bool,
	allowedFast *bm.LockFreeBitmap,
	result *PickResult,
) {
	// Collect all pieces the peer has that we want.
	var candidates []uint32
	for _, pi := range pp.pieces {
		if !bitfield.Contains(pi) {
			continue
		}
		if choked && !allowedFast.Contains(pi) {
			continue
		}
		if pp.allBlocksResponded(pi) {
			continue
		}
		candidates = append(candidates, pi)
	}
	if len(candidates) == 0 {
		return
	}

	// Sort by availability ascending (rarest first).
	sort.Slice(candidates, func(i, j int) bool {
		return pp.availability[candidates[i]] < pp.availability[candidates[j]]
	})

	// Exclude the top 25% rarest and bottom 25% most common.
	// Pick randomly from the middle 50% to avoid extremes.
	lo := len(candidates) / 4
	hi := len(candidates) * 3 / 4
	if hi <= lo {
		lo = 0
		hi = len(candidates)
	}

	pieceIdx := candidates[lo+rand.IntN(hi-lo)]

	// Pick the first free block from the chosen piece.
	// In startup mode no blocks are in flight, so the first block is always free.
	result.FreeBlocks = append(result.FreeBlocks, PieceBlock{pieceIdx, 0})
}

// pickBlocksFromPiece picks free blocks from a specific piece.
func (pp *PiecePicker) pickBlocksFromPiece(
	pieceIndex uint32,
	numBlocks *int,
	result *PickResult,
) {
	if *numBlocks <= 0 {
		return
	}

	idx := pp.blockInfoIdx(pieceIndex)
	count := int(pp.numBlocksInPiece(pieceIndex))

	for i := range count {
		switch pp.blockInfos.get(idx + i) {
		case blockStateNone:
			result.FreeBlocks = append(result.FreeBlocks, PieceBlock{pieceIndex, uint32(i)})
			*numBlocks--
			if *numBlocks <= 0 {
				return
			}
		case blockStateRequested:
			result.BusyBlocks = append(result.BusyBlocks, PieceBlock{pieceIndex, uint32(i)})
		}
	}
}

// findDownloadingPiece finds a downloading piece by index.
// downloadingPieces must be kept sorted by index (AddDownloadingPiece does this).
// Caller must hold pp.mu.
func (pp *PiecePicker) findDownloadingPiece(pieceIndex uint32) *downloadingPiece {
	i, found := slices.BinarySearchFunc(pp.downloadingPieces, pieceIndex,
		func(dp downloadingPiece, idx uint32) int {
			if dp.index < idx {
				return -1
			}
			if dp.index > idx {
				return 1
			}
			return 0
		})
	if found {
		return &pp.downloadingPieces[i]
	}
	return nil
}

// isAlreadyPicked checks if a piece already has blocks in the result.
// Caller must hold pp.mu.
func (pp *PiecePicker) isAlreadyPicked(pieceIndex uint32, result *PickResult) bool {
	for _, fb := range result.FreeBlocks {
		if fb.PieceIndex == pieceIndex {
			return true
		}
	}
	return false
}

// AddDownloadingPiece adds a piece to the downloading set.
func (pp *PiecePicker) AddDownloadingPiece(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.addDownloadingPieceUnsafe(pieceIndex)
}

// addDownloadingPieceUnsafe adds a piece while pp.mu is held.
func (pp *PiecePicker) addDownloadingPieceUnsafe(pieceIndex uint32) {
	if pp.findDownloadingPiece(pieceIndex) != nil {
		return
	}

	pp.downloadingPieces = append(pp.downloadingPieces, downloadingPiece{
		index:         pieceIndex,
		infoIdx:       pp.blockInfoIdx(pieceIndex),
		blocksInPiece: pp.numBlocksInPiece(pieceIndex),
	})
	// keep sorted by index for binary search
	slices.SortFunc(pp.downloadingPieces, func(a, b downloadingPiece) int {
		if a.index < b.index {
			return -1
		}
		if a.index > b.index {
			return 1
		}
		return 0
	})
	pp.dirty = true
	pp.partialsDirty = true
}

// RemoveDownloadingPiece removes a piece from the downloading set.
func (pp *PiecePicker) RemoveDownloadingPiece(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			pp.downloadingPieces = slices.Delete(pp.downloadingPieces, i, i+1)
			pp.dirty = true
			return
		}
	}
}

// CountBusyBlocks returns the number of busy (requested) blocks in a piece.
func (pp *PiecePicker) CountBusyBlocks(pieceIndex uint32) int {
	if pp == nil {
		return 0
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	return pp.blockInfos.countRequested(idx, int(pp.numBlocksInPiece(pieceIndex)))
}

// PickerStats holds summary block-state counts for debug output.
type PickerStats struct {
	DiagAt          time.Time
	OpenPieces      int
	Downloading     int
	RequestedBlocks int
	RespondedBlocks int
	FreeBlocks      int
	DownloadQueue   int
	ActiveClaims    int
	DuplicateClaims int
	StaleAccepts    uint64
	StaleReleases   uint64

	// Diagnostic counters from the last sampled empty PickPieces call.
	DiagNumWant            int
	DiagSkippedResponded   int
	DiagSkippedBitfield    int
	DiagSkippedChoked      int
	DiagSkippedBlocked     int
	DiagSkippedDownloading int
	DiagDirty              bool
	DiagFreeBlocks         int
}

type DownloadingPieceInfo struct {
	Blocks     int
	Responded  int
	Requested  int
	Free       int
	Index      uint32
	HashPassed bool
	Locked     bool
}

// DebugDownloadingPieces returns detail about every in-flight piece.
func (pp *PiecePicker) DebugDownloadingPieces() []DownloadingPieceInfo {
	if pp == nil {
		return nil
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	result := make([]DownloadingPieceInfo, 0, len(pp.downloadingPieces))
	for _, dp := range pp.downloadingPieces {
		idx := pp.blockInfoIdx(dp.index)
		blocksInPiece := int(dp.blocksInPiece)
		di := DownloadingPieceInfo{
			Index:      dp.index,
			Blocks:     blocksInPiece,
			HashPassed: dp.passedHashCheck,
			Locked:     dp.locked,
		}
		for i := range blocksInPiece {
			switch pp.blockInfos.get(idx + i) {
			case blockStateNone:
				di.Free++
			case blockStateRequested:
				di.Requested++
			case blockStateResponded:
				di.Responded++
			}
		}
		result = append(result, di)
	}
	return result
}

// DebugStats returns picker state summary for debugging.
func (pp *PiecePicker) DebugStats() PickerStats {
	if pp == nil {
		return PickerStats{}
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	st := PickerStats{
		OpenPieces:      len(pp.pieces),
		Downloading:     len(pp.downloadingPieces),
		DownloadQueue:   pp.downloadQueueSize,
		ActiveClaims:    len(pp.activeClaims),
		DuplicateClaims: len(pp.activeClaims) - len(pp.claimsByBlock),
		StaleAccepts:    pp.staleAccepts,
		StaleReleases:   pp.staleReleases,
		DiagAt:          pp.lastDiagAt,

		DiagNumWant:            pp.diagNumWant,
		DiagSkippedResponded:   pp.diagSkippedResponded,
		DiagSkippedBitfield:    pp.diagSkippedBitfield,
		DiagSkippedChoked:      pp.diagSkippedChoked,
		DiagSkippedBlocked:     pp.diagSkippedBlocked,
		DiagSkippedDownloading: pp.diagSkippedDownloading,
		DiagDirty:              pp.diagDirty,
		DiagFreeBlocks:         pp.diagFreeBlocks,
	}

	for pi := range pp.numPieces {
		idx := pp.blockInfoIdx(pi)
		nb := pp.numBlocksInPiece(pi)
		for i := range int(nb) {
			switch pp.blockInfos.get(idx + i) {
			case blockStateNone:
				st.FreeBlocks++
			case blockStateRequested:
				st.RequestedBlocks++
			case blockStateResponded:
				st.RespondedBlocks++
			}
		}
	}
	return st
}

// ResetPiece resets all blocks in a piece to state none (for hash check failure).
func (pp *PiecePicker) ResetPiece(pieceIndex uint32) {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	idx := pp.blockInfoIdx(pieceIndex)
	nb := pp.numBlocksInPiece(pieceIndex)
	pp.invalidatePieceClaimsUnsafe(pieceIndex)

	// Count before reset: blocks that were NOT None get added to freeBlocks.
	noneBefore := pp.blockInfos.countNone(idx, int(nb))
	pp.freeBlocks += int(nb) - noneBefore

	for i := range int(nb) {
		if pp.blockInfos.get(idx+i) == blockStateRequested {
			pp.downloadQueueSize--
		}
		pp.blockInfos.set(idx+i, blockStateNone)
	}
	pp.respondedBlocks[pieceIndex] = 0
	// Reset downloadingPiece counters instead of removing it.
	// Keeping the piece in downloadingPieces prevents pickPieces from
	// entering startup mode (numCompletedPieces==0 && len(downloadingPieces)==0),
	// which would randomly pick a piece instead of prioritizing the failed one.
	if dp := pp.findDownloadingPiece(pieceIndex); dp != nil {
		dp.responded = 0
		dp.requested = 0
		pp.partialsDirty = true
	}

	// Re-add piece to candidates if it was removed by rebuildPriorities.
	// Without this, hash-failed pieces are lost from the candidate list
	// and never re-downloaded.
	found := slices.Contains(pp.pieces, pieceIndex)
	if !found {
		pp.pieces = append(pp.pieces, pieceIndex)
	}

	// Clear exclusive ownership so the piece can be re-claimed.
	delete(pp.parolePieceOwners, pieceIndex)

	pp.dirty = true
}

// ResetAll resets all block states to none and clears downloading pieces.
// Used during recheck (AsyncCheck) when completedBm is cleared.
func (pp *PiecePicker) ResetAll() {
	if pp == nil {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.blockInfos.resetAll()
	clear(pp.respondedBlocks)
	pp.downloadingPieces = pp.downloadingPieces[:0]
	pp.downloadQueueSize = 0
	pp.freeBlocks = int(pp.blocksPerPiece)*int(pp.numPieces-1) + int(pp.numBlocksInPiece(pp.numPieces-1))
	pp.numCompletedPieces = 0
	clear(pp.parolePieceOwners)
	clear(pp.activeClaims)
	clear(pp.claimsByBlock)
	clear(pp.claimsByPeer)
	pp.dirty = true
	pp.partialsDirty = true
}

func (pp *PiecePicker) removeDownloadingPieceUnsafe(pieceIndex uint32) {
	for i := range pp.downloadingPieces {
		if pp.downloadingPieces[i].index == pieceIndex {
			pp.downloadingPieces = slices.Delete(pp.downloadingPieces, i, i+1)
			pp.dirty = true
			pp.partialsDirty = true
			return
		}
	}
}

// DebugDump writes detailed picker state to string.
func (pp *PiecePicker) DebugDump() string {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.debugDumpUnsafe()
}

// debugDumpUnsafe writes detailed picker state to buf. Caller must hold pp.mu.
func (pp *PiecePicker) debugDumpUnsafe() string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "numPieces=%d numWantLeft=%d downloadQueueSize=%d\n",
		pp.numPieces, pp.numWantLeft, pp.downloadQueueSize)
	fmt.Fprintf(&buf, "pieces(available): %d, downloadingPieces: %d\n",
		len(pp.pieces), len(pp.downloadingPieces))

	fmt.Fprintf(&buf, "--- pieces ---\n")
	for _, pi := range pp.pieces {
		fmt.Fprintf(&buf, "  %d avail=%d\n", pi, pp.availability[pi])
	}

	fmt.Fprintf(&buf, "--- downloadingPieces ---\n")
	for _, dp := range pp.downloadingPieces {
		idx := pp.blockInfoIdx(dp.index)
		nb := pp.numBlocksInPiece(dp.index)
		var free, req, resp int
		for i := range int(nb) {
			switch pp.blockInfos.get(idx + i) {
			case blockStateNone:
				free++
			case blockStateRequested:
				req++
			case blockStateResponded:
				resp++
			}
		}
		state := fmt.Sprintf("f=%d r=%d d=%d", free, req, resp)
		if dp.passedHashCheck {
			state += " hashOK"
		}
		fmt.Fprintf(&buf, "  %d: %s (in-piece req=%d resp=%d)\n",
			dp.index, state, dp.requested, dp.responded)
	}

	fmt.Fprintf(&buf, "--- all pieces by block state ---\n")
	for pi := range pp.numPieces {
		if !pp.missingBm.Contains(pi) {
			continue
		}
		idx := pp.blockInfoIdx(pi)
		nb := pp.numBlocksInPiece(pi)
		var free, req, resp int
		for i := range int(nb) {
			switch pp.blockInfos.get(idx + i) {
			case blockStateNone:
				free++
			case blockStateRequested:
				req++
			case blockStateResponded:
				resp++
			}
		}
		inDl := pp.findDownloadingPiece(pi) != nil
		fmt.Fprintf(&buf, "  %d f=%d r=%d d=%d inDl=%v avail=%d\n",
			pi, free, req, resp, inDl, pp.availability[pi])
	}

	return buf.String()
}
