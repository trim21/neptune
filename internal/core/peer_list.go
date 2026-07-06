// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/netip"
	"slices"
	"sync"
)

// peerSource mirrors libtorrent's peer_source_flags_t.
type peerSource uint8

const (
	peerSourceTracker    peerSource = 1 << 0
	peerSourceDHT        peerSource = 1 << 1
	peerSourcePEX        peerSource = 1 << 2
	peerSourceLSD        peerSource = 1 << 3
	peerSourceResumeData peerSource = 1 << 4
	peerSourceIncoming   peerSource = 1 << 5
)

// sourceRank returns a priority score for a peer source.
// Higher rank = higher priority for connection.
// Mirrors libtorrent's source_rank().
func sourceRank(source peerSource) int {
	r := 0
	if source&peerSourceTracker != 0 {
		r |= 1 << 5
	}
	if source&peerSourceLSD != 0 {
		r |= 1 << 4
	}
	if source&peerSourceDHT != 0 {
		r |= 1 << 3
	}
	if source&peerSourcePEX != 0 {
		r |= 1 << 2
	}
	return r
}

// persistentPeer mirrors libtorrent's torrent_peer — permanent peer metadata
// that survives connection/disconnection cycles.
type persistentPeer struct {
	connection       *Peer
	addrPort         netip.AddrPort
	lastErr          string
	lastSeen         int64
	priority         uint32
	cachedSourceRank int
	failcount        uint8
	source           peerSource
	connectable      bool
	seed             bool
	hadTrans         bool
	dialing          bool
}

// isConnectCandidate returns true if this peer is eligible for connection.
// Mirrors libtorrent's is_connect_candidate().
func (p *persistentPeer) isConnectCandidate(finished bool, maxFailcount int) bool {
	if p.connection != nil {
		return false
	}
	if p.dialing {
		return false
	}
	if !p.connectable {
		return false
	}
	if p.seed && finished {
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

// peerList mirrors libtorrent's peer_list — persistent storage of all known
// peers with a pre-computed connect candidate cache.
//
// Peers are stored sorted by address for O(log n) lookup. Candidates are kept
// in a small sorted vector (max 10) that is lazily populated by
// findConnectCandidates when empty.
type peerList struct {
	d                    *Download
	candidateCache       []candidateEntry // sorted cache of top candidates (max 10)
	peers                []*persistentPeer
	numConnectCandidates int
	roundRobin           int
	maxFailcount         int
	minReconnectTime     int64
	mu                   sync.Mutex
	finished             bool
}

const candidateCount = 50

func newPeerList(d *Download) *peerList {
	return &peerList{
		d:                d,
		candidateCache:   make([]candidateEntry, 0, candidateCount),
		maxFailcount:     3,
		minReconnectTime: 60,
	}
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
func (pl *peerList) addPeer(addr netip.AddrPort, source peerSource, connectable bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if found {
		p := pl.peers[idx]
		pl.updatePeerLocked(p, source, connectable)
		return
	}

	p := &persistentPeer{
		addrPort:         addr,
		source:           source,
		cachedSourceRank: sourceRank(source),
		connectable:      connectable,
		lastSeen:         0,
		priority:         pl.d.c.PeerPriority(addr),
	}

	pl.peers = slices.Insert(pl.peers, idx, p)

	if p.isConnectCandidate(pl.finished, pl.maxFailcount) {
		pl.numConnectCandidates++
		pl.insertCandidateCacheLocked(p)
	}
}

// updatePeerLocked updates an existing peer's metadata. Caller holds pl.mu.
// Mirrors libtorrent's peer_list::update_peer().
func (pl *peerList) updatePeerLocked(p *persistentPeer, source peerSource, connectable bool) {
	wasConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)

	p.source |= source
	p.cachedSourceRank = sourceRank(p.source)
	if connectable {
		p.connectable = true
	}

	if source&peerSourceTracker != 0 {
		p.failcount = 0
	}

	isConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)
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
	idx := sortSearch(len(pl.peers), func(i int) bool {
		return addrLess(addr, pl.peers[i].addrPort)
	})
	if idx < len(pl.peers) && pl.peers[idx].addrPort == addr {
		return idx, true
	}
	return idx, false
}

func sortSearch(n int, f func(int) bool) int {
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		if !f(h) {
			i = h + 1
		} else {
			j = h
		}
	}
	return i
}

func addrLess(a, b netip.AddrPort) bool {
	ab := a.Addr().Compare(b.Addr())
	if ab < 0 {
		return true
	}
	if ab > 0 {
		return false
	}
	return a.Port() < b.Port()
}

// addOrUpdateIncoming ensures a peer entry exists for an incoming connection.
// Returns true if this is a duplicate connection (peer already has connection).
func (pl *peerList) addOrUpdateIncoming(addr netip.AddrPort, sessionTime int64, conn *Peer) (rejectDuplicate bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if found {
		pp := pl.peers[idx]
		if pp.connection != nil {
			return true // duplicate, reject
		}
		wasConnCand := pp.isConnectCandidate(pl.finished, pl.maxFailcount)
		pp.connection = conn
		pp.lastSeen = sessionTime
		pp.source |= peerSourceIncoming
		pp.cachedSourceRank = sourceRank(pp.source)
		pp.connectable = true
		if wasConnCand {
			pl.numConnectCandidates--
		}
		return false
	}

	pp := &persistentPeer{
		addrPort:         addr,
		source:           peerSourceIncoming,
		cachedSourceRank: sourceRank(peerSourceIncoming),
		connectable:      false,
		connection:       conn,
		lastSeen:         sessionTime,
		priority:         pl.d.c.PeerPriority(addr),
	}
	pl.peers = slices.Insert(pl.peers, idx, pp)
	return false
}

// newConnection attaches a connection to an existing peer entry.
// Returns false if the peer wasn't found in the list.
func (pl *peerList) newConnection(addr netip.AddrPort, conn *Peer, sessionTime int64) bool {
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

	wasConnCand := pp.isConnectCandidate(pl.finished, pl.maxFailcount)

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
func (pl *peerList) connectionClosed(addr netip.AddrPort, sessionTime int64, hadTrans bool, failed bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	idx, found := pl.findPeer(addr)
	if !found {
		return
	}

	pp := pl.peers[idx]
	pp.connection = nil
	pp.hadTrans = pp.hadTrans || hadTrans

	// Only update lastSeen if no transfer happened.
	// Peers with hadTrans keep their old lastSeen, giving them priority
	// in comparePeer (lower lastSeen = not recently tried = preferred).
	if !hadTrans {
		pp.lastSeen = sessionTime
	}

	if failed {
		if pp.failcount < 31 {
			pp.failcount++
		}
	}

	if pp.isConnectCandidate(pl.finished, pl.maxFailcount) {
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

		if !pp.isConnectCandidate(pl.finished, pl.maxFailcount) {
			continue
		}

		// Reconnect time check: failcount-based backoff.
		if pp.lastSeen > 0 {
			backoff := int64(pp.failcount+1) * pl.minReconnectTime
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

// connectPeers returns up to n best connect candidates in a single locked call.
// Mirrors libtorrent's connect_one_peer logic:
// 1. Clean cache (remove non-candidates)
// 2. Refill if empty via findConnectCandidates
// 3. Pop from front.
func (pl *peerList) connectPeers(sessionTime int64, n int) []*persistentPeer {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	// Clean cache: remove entries that are no longer connect candidates.
	cleaned := pl.candidateCache[:0]
	for _, entry := range pl.candidateCache {
		if entry.p.isConnectCandidate(pl.finished, pl.maxFailcount) {
			cleaned = append(cleaned, entry)
		}
	}
	pl.candidateCache = cleaned

	if len(pl.candidateCache) == 0 {
		pl.findConnectCandidates(sessionTime)
		if len(pl.candidateCache) == 0 {
			return nil
		}
	}

	result := make([]*persistentPeer, 0, min(n, len(pl.candidateCache)))
	for len(result) < n && len(pl.candidateCache) > 0 {
		pp := pl.candidateCache[0].p
		// Shift left to preserve backing array capacity.
		remaining := copy(pl.candidateCache, pl.candidateCache[1:])
		pl.candidateCache = pl.candidateCache[:remaining]
		pp.dialing = true
		result = append(result, pp)
	}

	return result
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

	wasConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)
	p.failcount++
	if wasConnCand && !p.isConnectCandidate(pl.finished, pl.maxFailcount) {
		pl.numConnectCandidates--
	}
}

// setFinished updates the finished flag and recalculates candidates.
func (pl *peerList) setFinished(v bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if pl.finished == v {
		return
	}
	pl.finished = v

	// Invalidate cache since candidate eligibility may change.
	pl.candidateCache = pl.candidateCache[:0]

	// recalculate candidates
	pl.numConnectCandidates = 0
	for _, p := range pl.peers {
		if p.isConnectCandidate(pl.finished, pl.maxFailcount) {
			pl.numConnectCandidates++
		}
	}
}

// numConnectCandidates returns the count (lock-free approximation).
func (pl *peerList) numCandidates() int {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return pl.numConnectCandidates
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

	wasConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)
	p.connectable = connectable
	isConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)

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

// peerTurnover disconnects up to 'count' slow peers to make room for new ones.
// Mirrors libtorrent's disconnect_peers with optimistic_disconnect.
func (pl *peerList) peerTurnover(count int) []*Peer {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	type connectedPeer struct {
		p          *persistentPeer
		uploadRate int64
	}
	var connected []connectedPeer
	for _, pp := range pl.peers {
		if pp.connection != nil && !pp.connection.closed.Load() {
			connected = append(connected, connectedPeer{
				p:          pp,
				uploadRate: pp.connection.pieceUploadRate.Status().CurRate,
			})
		}
	}

	slices.SortFunc(connected, func(a, b connectedPeer) int {
		if a.uploadRate < b.uploadRate {
			return -1
		}
		if a.uploadRate > b.uploadRate {
			return 1
		}
		return 0
	})

	toDisconnect := make([]*Peer, 0, min(count, len(connected)))
	for i := range min(count, len(connected)) {
		toDisconnect = append(toDisconnect, connected[i].p.connection)
	}

	return toDisconnect
}
