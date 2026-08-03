// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"math/rand/v2"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"go.uber.org/atomic"

	"neptune/internal/client/tracker"
)

// persistentPeer mirrors libtorrent's torrent_peer — permanent peer metadata
// that survives connection/disconnection cycles.
type persistentPeer struct {
	connection       Peer
	addrPort         netip.AddrPort
	lastErr          string
	lastSeen         int64
	priority         uint32
	cachedSourceRank int
	failcount        uint8
	source           tracker.PeerSource
	connectable      bool
	hadTrans         bool
	dialing          bool
	seed             bool
}

// isConnectCandidate returns true if this peer is eligible for connection.
// Mirrors libtorrent's is_connect_candidate().
func (p *persistentPeer) isConnectCandidate(maxFailcount int) bool {
	if p.connection != nil {
		return false
	}
	if p.dialing {
		return false
	}
	if !p.connectable {
		return false
	}
	if int(p.failcount) >= maxFailcount {
		return false
	}
	return true
}

// candidateEntry wraps a persistentPeer for the sorted candidate cache.
type candidateEntry struct {
	p *persistentPeer
}

// comparePeer returns true if lhs is a better connect candidate than rhs.
// Mirrors libtorrent's compare_peer().
func comparePeer(lhs, rhs *persistentPeer) bool {
	// prefer peers with lower failcount
	if lhs.failcount != rhs.failcount {
		return lhs.failcount < rhs.failcount
	}

	// prefer peers not recently tried (lower lastSeen = longer ago or never tried)
	// This replaces libtorrent's last_connected comparison and handles fast reconnect:
	// peers with hadTrans keep their old lastSeen, so they get prioritized.
	if lhs.lastSeen != rhs.lastSeen {
		return lhs.lastSeen < rhs.lastSeen
	}

	// source rank (tracker > LSD > DHT > PEX)
	if lhs.cachedSourceRank != rhs.cachedSourceRank {
		return lhs.cachedSourceRank > rhs.cachedSourceRank
	}

	// BEP40 priority (higher is better for swarm diversity)
	if lhs.priority != rhs.priority {
		return lhs.priority > rhs.priority
	}

	return false
}

// peerList owns all peer state for a torrent: the persistent peer metadata
// with a pre-computed connect candidate cache, plus the registry of active
// connections. Download delegates every peer lifecycle operation here, so a
// connection is recorded (and cleaned up) in exactly one place.
//
// Persistent peers are stored sorted by address for O(log n) lookup. Candidates
// are kept in a small sorted vector (max 10) that is lazily populated by
// findConnectCandidates when empty. Active connections are keyed both by
// unique peer ID and by address (address dedup rejects duplicate connections).
type peerList struct {
	activeByID           *xsync.Map[uint64, Peer]
	activeByAddr         *xsync.Map[netip.AddrPort, Peer]
	d                    *Download
	bannedAddrs          map[netip.Addr]int64
	incomingConnections  map[netip.AddrPort]uint32
	peers                []*persistentPeer
	candidateCache       []candidateEntry
	idCounter            atomic.Uint64
	numConnectCandidates int
	roundRobin           int
	minReconnectTime     int64
	mu                   sync.Mutex
}

// maxFailcount returns the dial-failure cap for the current download state.
// Seeding dials rarely pay off (leechers are the only useful target), so we
// give up on a peer sooner than when downloading.
func (pl *peerList) maxFailcount() int {
	if pl.d.HasState(Seeding) {
		return 3
	}
	return 10
}

// finished reports whether the download is seeding: we are complete, so the
// only peers worth connecting to are leechers.
func (pl *peerList) finished() bool {
	return pl.d.HasState(Seeding)
}

const candidateCount = 50

func newPeerList(d *Download) *peerList {
	return &peerList{
		d:                   d,
		activeByID:          xsync.NewMap[uint64, Peer](),
		activeByAddr:        xsync.NewMap[netip.AddrPort, Peer](),
		candidateCache:      make([]candidateEntry, 0, candidateCount),
		bannedAddrs:         make(map[netip.Addr]int64),
		incomingConnections: make(map[netip.AddrPort]uint32),
		minReconnectTime:    60,
	}
}

// ── Active connection registry ────────────────────────────────────────

// AllocID returns a unique peer ID for a new connection.
func (pl *peerList) AllocID() uint64 {
	return pl.idCounter.Add(1)
}

// Register records an active connection under both its unique ID and its
// address. Returns false when another active connection already owns the
// address — the caller must close the new connection; its by-ID entry is
// removed by the subsequent Unregister.
func (pl *peerList) Register(p Peer) bool {
	pl.activeByID.Store(p.ID(), p)
	_, loaded := pl.activeByAddr.LoadOrStore(p.Addr(), p)
	return !loaded
}

// Unregister removes a connection from the active registry. Only the peer
// that actually owns its address entry is removed from the address map, so a
// peer that lost the duplicate-connection race only clears its by-ID entry.
func (pl *peerList) Unregister(p Peer) {
	if actual, ok := pl.activeByAddr.Load(p.Addr()); ok && actual == p {
		pl.activeByAddr.Delete(p.Addr())
	}
	pl.activeByID.Delete(p.ID())
}

// Range iterates active connections by peer ID.
func (pl *peerList) Range(fn func(uint64, Peer) bool) {
	pl.activeByID.Range(fn)
}

// Load returns the active connection with the given peer ID.
func (pl *peerList) Load(id uint64) (Peer, bool) {
	return pl.activeByID.Load(id)
}

// Size returns the number of active connections.
func (pl *peerList) Size() int {
	return pl.activeByID.Size()
}

// isAddrBanned checks whether addr is currently banned for this torrent.
func (pl *peerList) isAddrBanned(addr netip.Addr) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.isAddrBannedLocked(addr, time.Now().Unix())
}

// insertCandidateCacheLocked inserts a peer into the sorted candidate cache
// if there's room or it's better than the worst cached candidate.
// Caller must hold pl.mu.
func (pl *peerList) insertCandidateCacheLocked(pp *persistentPeer) {
	// If cache is full and worst candidate is better, skip.
	if len(pl.candidateCache) == candidateCount &&
		comparePeer(pl.candidateCache[len(pl.candidateCache)-1].p, pp) {
		return
	}

	// Trim cache if at capacity.
	if len(pl.candidateCache) >= candidateCount {
		pl.candidateCache = pl.candidateCache[:candidateCount-1]
	}

	// Find insertion point (sorted by comparePeer).
	insertIdx := len(pl.candidateCache)
	for i, entry := range pl.candidateCache {
		if comparePeer(pp, entry.p) {
			insertIdx = i
			break
		}
	}

	// Grow and shift right.
	pl.candidateCache = pl.candidateCache[:len(pl.candidateCache)+1]
	copy(pl.candidateCache[insertIdx+1:], pl.candidateCache[insertIdx:len(pl.candidateCache)-1])
	pl.candidateCache[insertIdx] = candidateEntry{p: pp}
}

// addPeer adds or updates a peer.
// Mirrors libtorrent's peer_list::add_peer().
func (pl *peerList) addPeer(addr netip.AddrPort, source tracker.PeerSource) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if found {
		p := pl.peers[idx]
		pl.updatePeerLocked(p, source)
		return
	}

	p := &persistentPeer{
		addrPort:         addr,
		source:           source,
		cachedSourceRank: tracker.SourceRank(source),
		connectable:      true,
		lastSeen:         0,
		priority:         pl.d.session.PeerPriority(addr),
	}

	pl.peers = slices.Insert(pl.peers, idx, p)

	if p.isConnectCandidate(pl.maxFailcount()) {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(p)
	}
}

// updatePeerLocked updates an existing peer's metadata. Caller holds pl.mu.
// Mirrors libtorrent's peer_list::update_peer().
func (pl *peerList) updatePeerLocked(p *persistentPeer, source tracker.PeerSource) {
	wasConnCand := p.isConnectCandidate(pl.maxFailcount())

	p.source |= source
	p.cachedSourceRank = tracker.SourceRank(p.source)
	p.connectable = true

	if source&tracker.PeerSourceTracker != 0 {
		p.failcount = 0
	}

	isConnCand := p.isConnectCandidate(pl.maxFailcount())
	if wasConnCand && !isConnCand {
		pl.numConnectCandidates--
	} else if !wasConnCand && isConnCand {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(p)
	}
}

// findPeer binary-searches for a peer by addrPort. Returns the index where
// it was found or should be inserted, and whether it was found.
func (pl *peerList) findPeer(addr netip.AddrPort) (int, bool) {
	n := len(pl.peers)
	lo, hi := 0, n
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		cmp := compareAddr(pl.peers[mid].addrPort, addr)
		if cmp < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < n && pl.peers[lo].addrPort == addr {
		return lo, true
	}
	return lo, false
}

func compareAddr(a, b netip.AddrPort) int {
	if r := a.Addr().Compare(b.Addr()); r != 0 {
		return r
	}
	if a.Port() < b.Port() {
		return -1
	}
	if a.Port() > b.Port() {
		return 1
	}
	return 0
}

// newConnection attaches a connection to an existing peer entry.
// Returns false if the peer wasn't found in the list.
func (pl *peerList) newConnection(addr netip.AddrPort, conn Peer, sessionTime int64) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if !found {
		return false
	}

	pp := pl.peers[idx]

	if pp.connection != nil {
		pp.dialing = false
		return false
	}

	wasConnCand := pp.isConnectCandidate(pl.maxFailcount())

	pp.dialing = false
	pp.connection = conn
	pp.lastSeen = sessionTime
	pp.connectable = true

	if wasConnCand {
		pl.numConnectCandidates--
	}

	return true
}

// connectionClosed is called when a peer connection closes.
// Mirrors libtorrent's peer_list::connection_closed().
// Does NOT touch candidateCache.
func (pl *peerList) connectionClosed(addr netip.AddrPort, conn Peer, sessionTime int64, hadTrans bool, failed bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if !found {
		return
	}

	pp := pl.peers[idx]
	if pp.connection != conn {
		return
	}
	pp.connection = nil
	pp.hadTrans = pp.hadTrans || hadTrans

	// Only update lastSeen if no transfer happened.
	// Peers with hadTrans keep their old lastSeen, giving them priority
	// in comparePeer (lower lastSeen = not recently tried = preferred).
	if !hadTrans {
		pp.lastSeen = sessionTime
	}

	// Don't penalize peers that already transferred data — transient
	// disconnects are likely network issues, not peer quality problems.
	if failed && !hadTrans {
		if pp.failcount < 31 {
			pp.failcount++
		}
	}

	if pp.isConnectCandidate(pl.maxFailcount()) {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(pp)
	}
}

// findConnectCandidates rebuilds the candidate cache by scanning the peer list.
// Inserts at most candidateCount (10) entries into a sorted slice.
// Mirrors libtorrent's peer_list::find_connect_candidates().
func (pl *peerList) findConnectCandidates(sessionTime int64) {
	if len(pl.peers) == 0 {
		return
	}

	if pl.roundRobin >= len(pl.peers) {
		pl.roundRobin = 0
	}

	// scan up to 300 peers starting from roundRobin
	maxIter := min(len(pl.peers), 300)
	for range maxIter {
		if pl.roundRobin >= len(pl.peers) {
			pl.roundRobin = 0
		}

		pp := pl.peers[pl.roundRobin]
		pl.roundRobin++

		if !pl.isConnectCandidateLocked(pp, sessionTime) {
			continue
		}

		// Reconnect time check: failcount-based backoff with jitter.
		// Jitter spreads retries uniformly in [base, 2*base), desynchronizing
		// attempts across downloads and preventing connection storms where
		// multiple downloads retry the same peers simultaneously.
		if pp.lastSeen > 0 {
			base := int64(pp.failcount+1) * pl.minReconnectTime
			backoff := base + rand.Int64N(base)
			if sessionTime-pp.lastSeen < backoff {
				continue
			}
		}

		// If cache is full and the worst cached candidate is better than pp, skip.
		if len(pl.candidateCache) == candidateCount &&
			comparePeer(pl.candidateCache[len(pl.candidateCache)-1].p, pp) {
			continue
		}

		// Trim cache if at capacity.
		if len(pl.candidateCache) >= candidateCount {
			pl.candidateCache = pl.candidateCache[:candidateCount-1]
		}

		// Insert sorted: find position and insert.
		insertIdx := len(pl.candidateCache)
		for i, entry := range pl.candidateCache {
			if comparePeer(pp, entry.p) {
				insertIdx = i
				break
			}
		}

		// Grow and shift right.
		pl.candidateCache = pl.candidateCache[:len(pl.candidateCache)+1]
		copy(pl.candidateCache[insertIdx+1:], pl.candidateCache[insertIdx:len(pl.candidateCache)-1])
		pl.candidateCache[insertIdx] = candidateEntry{p: pp}
	}
}

// prepareCandidateCacheLocked removes stale entries and refills an empty
// candidate cache. Caller must hold pl.mu.
func (pl *peerList) prepareCandidateCacheLocked(sessionTime int64) bool {
	// Clean cache: remove entries that are no longer connect candidates.
	cleaned := pl.candidateCache[:0]
	for _, entry := range pl.candidateCache {
		if pl.isConnectCandidateLocked(entry.p, sessionTime) {
			cleaned = append(cleaned, entry)
		}
	}
	pl.candidateCache = cleaned

	if len(pl.candidateCache) == 0 {
		pl.findConnectCandidates(sessionTime)
	}
	return len(pl.candidateCache) > 0
}

// hasConnectWork avoids joining the global DialSem FIFO when this download has
// no immediately eligible work. Selection and capacity are checked again after
// acquisition.
func (pl *peerList) hasConnectWork(sessionTime int64, maxConnections int) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	incoming, outgoing := pl.connectionCountsLocked()
	return incoming+outgoing < maxConnections && pl.prepareCandidateCacheLocked(sessionTime)
}

// pickConnectCandidate atomically re-checks per-download capacity and marks
// one candidate dialing after this download has acquired its global dial turn.
func (pl *peerList) pickConnectCandidate(sessionTime int64, maxConnections int) *persistentPeer {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	incoming, outgoing := pl.connectionCountsLocked()
	if incoming+outgoing >= maxConnections || !pl.prepareCandidateCacheLocked(sessionTime) {
		return nil
	}

	pp := pl.candidateCache[0].p
	remaining := copy(pl.candidateCache, pl.candidateCache[1:])
	pl.candidateCache = pl.candidateCache[:remaining]
	pp.dialing = true
	return pp
}

// connectionCountsLocked derives connection capacity from peer-list-owned
// lifecycle state. Caller must hold pl.mu.
func (pl *peerList) connectionCountsLocked() (incoming, outgoing int) {
	for _, count := range pl.incomingConnections {
		incoming += int(count)
	}
	for _, pp := range pl.peers {
		if pp.dialing || pp.connection != nil {
			outgoing++
		}
	}
	return incoming, outgoing
}

func (pl *peerList) connectionCount() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	incoming, outgoing := pl.connectionCountsLocked()
	return incoming + outgoing
}

// isConnectCandidateLocked applies peer-list-owned availability state in
// addition to persistent peer metadata. Caller holds pl.mu.
func (pl *peerList) isConnectCandidateLocked(pp *persistentPeer, sessionTime int64) bool {
	if !pp.isConnectCandidate(pl.maxFailcount()) {
		return false
	}
	// When seeding, a peer that has the full piece set has nothing to give us
	// and needs nothing from us — connecting is pure waste (mirrors
	// libtorrent's ((p.seed || p.upload_only) && m_finished) rule).
	if pl.finished() && pp.seed {
		return false
	}
	if pl.incomingConnections[pp.addrPort] > 0 {
		return false
	}
	return !pl.isAddrBannedLocked(pp.addrPort.Addr(), sessionTime)
}

func (pl *peerList) isAddrBannedLocked(addr netip.Addr, sessionTime int64) bool {
	expires, ok := pl.bannedAddrs[addr]
	if !ok {
		return false
	}
	if sessionTime >= expires {
		delete(pl.bannedAddrs, addr)
		return false
	}
	return true
}

// canDial reports whether an already-selected candidate is still eligible to
// start a transport connection. The dialing flag is intentionally ignored.
func (pl *peerList) canDial(pp *persistentPeer, sessionTime int64) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pp.connection != nil || !pp.connectable {
		return false
	}
	if pl.incomingConnections[pp.addrPort] > 0 {
		return false
	}
	return !pl.isAddrBannedLocked(pp.addrPort.Addr(), sessionTime)
}

// banAddr prevents outbound dials to addr until expires. Download owns the
// policy decision; peerList owns the candidate availability state.
func (pl *peerList) banAddr(addr netip.Addr, expires time.Time) {
	pl.mu.Lock()
	pl.bannedAddrs[addr] = expires.Unix()
	pl.mu.Unlock()
}

// incomingConnectionOpened prevents an existing or later-discovered
// candidate from being dialed while an inbound connection for that address is
// alive. The counter handles overlapping inbound handshakes.
func (pl *peerList) incomingConnectionOpened(addr netip.AddrPort) {
	pl.mu.Lock()
	pl.incomingConnections[addr]++
	pl.mu.Unlock()
}

// tryOpenIncoming atomically admits an incoming connection. Downloads with a
// limit of at least two always keep one lane available for outgoing dialing,
// preventing incoming peers from filling every per-download slot.
func (pl *peerList) tryOpenIncoming(addr netip.AddrPort, maxConnections int) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if maxConnections <= 0 {
		return false
	}
	incoming, outgoing := pl.connectionCountsLocked()
	if incoming+outgoing >= maxConnections {
		return false
	}
	if maxConnections > 1 && outgoing == 0 && incoming >= maxConnections-1 {
		return false
	}
	pl.incomingConnections[addr]++
	return true
}

func (pl *peerList) incomingConnectionClosed(addr netip.AddrPort) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if pl.incomingConnections[addr] <= 1 {
		delete(pl.incomingConnections, addr)
		return
	}
	pl.incomingConnections[addr]--
}

// clearDialing clears the dialing flag for a peer. Called when a candidate is
// skipped (already connected or semaphore full) and won't actually be dialed.
func (pl *peerList) clearDialing(p *persistentPeer) {
	pl.mu.Lock()
	p.dialing = false
	pl.mu.Unlock()
}

// incFailcount increments a peer's failcount. Called when a connection attempt fails.
func (pl *peerList) incFailcount(p *persistentPeer, errStr string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	p.dialing = false
	p.lastErr = errStr

	if p.failcount == 31 {
		return
	}

	wasConnCand := p.isConnectCandidate(pl.maxFailcount())
	p.failcount++
	if wasConnCand && !p.isConnectCandidate(pl.maxFailcount()) {
		pl.numConnectCandidates--
	}
}

// numConnectCandidates returns the count (lock-free approximation).
func (pl *peerList) numCandidates() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.numConnectCandidates
}

// updatePeerSeed records that a connected peer has the full piece set (seed).
// Kept across disconnections so a seeding download never re-dials a known
// seed. Called while the peer is connected; the flag survives the disconnect.
func (pl *peerList) updatePeerSeed(addr netip.AddrPort, seed bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if !found {
		return
	}

	p := pl.peers[idx]
	if p.seed == seed {
		return
	}

	wasConnCand := p.isConnectCandidate(pl.maxFailcount())
	p.seed = seed
	isConnCand := p.isConnectCandidate(pl.maxFailcount())

	if wasConnCand && !isConnCand {
		pl.numConnectCandidates--
	} else if !wasConnCand && isConnCand {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(p)
	}
}

// updateConnectable updates a peer's connectable flag when they advertise a port.
func (pl *peerList) updateConnectable(addr netip.AddrPort, connectable bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if !found {
		return
	}

	p := pl.peers[idx]
	if p.connectable == connectable {
		return
	}

	wasConnCand := p.isConnectCandidate(pl.maxFailcount())
	p.connectable = connectable
	isConnCand := p.isConnectCandidate(pl.maxFailcount())

	if wasConnCand && !isConnCand {
		pl.numConnectCandidates--
	} else if !wasConnCand && isConnCand {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(p)
	}
}

// hasPeer checks if a peer exists in the list.
func (pl *peerList) hasPeer(addr netip.AddrPort) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	_, found := pl.findPeer(addr)
	return found
}

// count returns the total number of peers in the list.
func (pl *peerList) count() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return len(pl.peers)
}

// TurnoverCutoff is the percentage of the connection limit at which peer
// eviction kicks in, applied at both the per-torrent and global levels.
// Mirrors libtorrent's peer_turnover_cutoff (default 90).
const TurnoverCutoff = 90

// fastPeerRateThreshold: peers transferring at or above this rate are never
// turned over — they are too valuable to gamble on an unknown candidate.
// Applies to both directions: fast downloads (a leecher's core bandwidth)
// and fast uploads (good tit-for-tat reciprocity).
const fastPeerRateThreshold = 1024 * 1024 // 1 MiB/s

// turnoverExemptScore marks a peer as exempt from turnover eviction.
// Sorted ascending, exempt peers always land at the end of the eviction
// candidate list.
const turnoverExemptScore = int64(1) << 30

// peerDisconnectScore calculates a disconnect priority score for a peer.
// Lower scores mean higher priority to disconnect.
func peerDisconnectScore(p Peer, weAreSeed bool) int64 {
	// Fast peers are always kept, even in the both-seeding case: a partial
	// seed may still be filling gaps, and evicting a fast peer for an
	// unknown candidate is a net loss.
	if p.DownloadRate() >= fastPeerRateThreshold || p.UploadRate() >= fastPeerRateThreshold {
		return turnoverExemptScore
	}
	// Both sides are seeding — no value in staying connected.
	if weAreSeed && p.IsSeed() {
		return 0
	}
	// New connection grace period — give fresh peers time to ramp up.
	if time.Since(p.ConnectedAt()) < 30*time.Second {
		return 200
	}

	// We have no interest in this peer's pieces.
	if !p.IsOurInterested() {
		return 1
	}

	// Peer is choking us and never gave us data.
	if p.IsChoking() && !p.HadTransfer() {
		return 2
	}

	// Peer is choking us but used to give data (might be temporary).
	if p.IsChoking() {
		return 50
	}

	// Peer is snubbed (repeated request timeouts).
	if p.IsSnubbed() {
		return 60
	}

	// Active connection — score by transfer rates.
	// Higher rates = higher score = less likely to disconnect.
	score := int64(100)
	score += p.DownloadRate() / 1024   // +1 per KB/s download
	score += p.UploadRate() / 1024 / 2 // +0.5 per KB/s upload (reciprocity)
	return score
}
