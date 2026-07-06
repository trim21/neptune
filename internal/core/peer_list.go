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
	connection  *Peer
	addrPort    netip.AddrPort
	lastSeen    int64
	failcount   uint8
	source      peerSource
	connectable bool
	seed        bool
	hadTrans    bool
	priority    uint32 // cached BEP40 priority, computed once on add
}

// isConnectCandidate returns true if this peer is eligible for connection.
// Mirrors libtorrent's is_connect_candidate().
func (p *persistentPeer) isConnectCandidate(finished bool, maxFailcount int) bool {
	if p.connection != nil {
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

// peerList mirrors libtorrent's peer_list — persistent storage of all known
// peers with pre-computed connect candidate cache.
//
// Peers are stored sorted by address for O(log n) lookup. A separate candidate
// cache holds connectable peers in priority order for O(1) pop.
type peerList struct {
	d                    *Download
	peers                []*persistentPeer
	candidateCache       []*persistentPeer
	numConnectCandidates int
	roundRobin           int
	maxFailcount         int
	minReconnectTime     int64
	mu                   sync.Mutex
	finished             bool
}

func newPeerList(d *Download) *peerList {
	return &peerList{
		d:                d,
		maxFailcount:     3,
		minReconnectTime: 60,
	}
}

// addPeer adds or updates a peer.
// Mirrors libtorrent's peer_list::add_peer().
func (pl *peerList) addPeer(addr netip.AddrPort, source peerSource, connectable bool) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	// binary search by addrPort
	idx, found := pl.findPeer(addr)
	if found {
		p := pl.peers[idx]
		pl.updatePeerLocked(p, source, connectable)
		return
	}

	p := &persistentPeer{
		addrPort:    addr,
		source:      source,
		connectable: connectable,
		lastSeen:    0,
		priority:    pl.d.c.PeerPriority(addr),
	}

	// insert maintaining sorted order
	pl.peers = slices.Insert(pl.peers, idx, p)

	if p.isConnectCandidate(pl.finished, pl.maxFailcount) {
		pl.numConnectCandidates++
		if !pl.connectableListContains(p) {
			pl.candidateCache = append(pl.candidateCache, p)
		}
	}
}

// updatePeerLocked updates an existing peer's metadata. Caller holds pl.mu.
// Mirrors libtorrent's peer_list::update_peer().
func (pl *peerList) updatePeerLocked(p *persistentPeer, source peerSource, connectable bool) {
	wasConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)

	p.source |= source
	if connectable {
		p.connectable = true
	}

	// if source is tracker, reset failcount so the peer gets a fresh chance
	if source&peerSourceTracker != 0 {
		p.failcount = 0
	}

	isConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)
	if wasConnCand && !isConnCand {
		pl.numConnectCandidates--
		pl.removeFromCandidateCache(p)
	} else if !wasConnCand && isConnCand {
		pl.numConnectCandidates++
		if !pl.connectableListContains(p) {
			pl.candidateCache = append(pl.candidateCache, p)
		}
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
	// compare address first
	ab := a.Addr().Compare(b.Addr())
	if ab < 0 {
		return true
	}
	if ab > 0 {
		return false
	}
	// same address, compare port
	return a.Port() < b.Port()
}

func (pl *peerList) removeFromCandidateCache(p *persistentPeer) {
	for i, c := range pl.candidateCache {
		if c == p {
			pl.candidateCache = slices.Delete(pl.candidateCache, i, i+1)
			return
		}
	}
}

func (pl *peerList) connectableListContains(p *persistentPeer) bool {
	return slices.Contains(pl.candidateCache, p)
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
		pp.connectable = true
		if wasConnCand {
			pl.numConnectCandidates--
			pl.removeFromCandidateCache(pp)
		}
		return false
	}

	pp := &persistentPeer{
		addrPort:    addr,
		source:      peerSourceIncoming,
		connectable: false, // incoming peers are unknown until they advertise port
		connection:  conn,
		lastSeen:    sessionTime,
		priority:    pl.d.c.PeerPriority(addr),
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
		return false
	}

	wasConnCand := pp.isConnectCandidate(pl.finished, pl.maxFailcount)

	pp.connection = conn
	pp.lastSeen = sessionTime
	pp.connectable = true

	if wasConnCand {
		pl.numConnectCandidates--
		pl.removeFromCandidateCache(pp)
	}

	return true
}

// connectionClosed is called when a peer connection closes.
// Mirrors libtorrent's peer_list::connection_closed().
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

	// Update lastSeen unless we allow fast reconnect
	// fast reconnect is used when the peer had transfers
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
		if !pl.connectableListContains(pp) {
			pl.candidateCache = append(pl.candidateCache, pp)
		}
	}
}

// connectOnePeer picks and returns the best connect candidate.
// Returns nil if no candidates are available.
// Mirrors libtorrent's peer_list::connect_one_peer().
func (pl *peerList) connectOnePeer(sessionTime int64) *persistentPeer {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if len(pl.candidateCache) == 0 {
		pl.findConnectCandidates(sessionTime)
		if len(pl.candidateCache) == 0 {
			return nil
		}
	}

	// pop the best candidate
	p := pl.candidateCache[0]
	pl.candidateCache = pl.candidateCache[1:]

	return p
}

// findConnectCandidates rebuilds the candidate cache by scanning the peer list.
// Mirrors libtorrent's peer_list::find_connect_candidates().
func (pl *peerList) findConnectCandidates(sessionTime int64) {
	pl.candidateCache = pl.candidateCache[:0]

	if len(pl.peers) == 0 {
		return
	}

	if pl.roundRobin >= len(pl.peers) {
		pl.roundRobin = 0
	}

	// scan up to 300 peers starting from roundRobin, collect all eligible
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

		// Check reconnect time: failcount-based backoff
		// Mirrors libtorrent: (failcount + 1) * min_reconnect_time
		if pp.lastSeen > 0 {
			backoff := int64(pp.failcount+1) * pl.minReconnectTime
			if sessionTime-pp.lastSeen < backoff {
				continue
			}
		}

		pl.candidateCache = append(pl.candidateCache, pp)
	}

	// sort by priority: failcount ascending, then source rank, then BEP40

	slices.SortFunc(pl.candidateCache, func(a, b *persistentPeer) int {
		// lower failcount first
		if a.failcount != b.failcount {
			return int(a.failcount) - int(b.failcount)
		}

		// local peers first (simplified: just check if private/link-local)
		// TODO: proper is_local check

		// source rank (tracker > DHT > LSD > PEX)
		ra := sourceRank(a.source)
		rb := sourceRank(b.source)
		if ra != rb {
			return rb - ra
		}

		// BEP40 priority (higher is better for swarm diversity)
		if a.priority != b.priority {
			return int(b.priority) - int(a.priority)
		}

		return 0
	})

	pl.numConnectCandidates = len(pl.candidateCache)
}

// incFailcount increments a peer's failcount. Called when a connection attempt fails.
func (pl *peerList) incFailcount(p *persistentPeer) {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	if p.failcount == 31 {
		return
	}

	wasConnCand := p.isConnectCandidate(pl.finished, pl.maxFailcount)
	p.failcount++
	if wasConnCand && !p.isConnectCandidate(pl.finished, pl.maxFailcount) {
		pl.numConnectCandidates--
		pl.removeFromCandidateCache(p)
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

	// recalculate candidates
	pl.numConnectCandidates = 0
	pl.candidateCache = pl.candidateCache[:0]
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
		pl.removeFromCandidateCache(p)
	} else if !wasConnCand && isConnCand {
		pl.numConnectCandidates++
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

	// collect connected peers sorted by upload rate (slowest first)
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

	// sort by upload rate ascending (slowest first)
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

// immediateCandidate returns a single peer that has hadTrans=true for immediate
// fast reconnect. Returns nil if none found.
func (pl *peerList) immediateCandidate() *persistentPeer {
	pl.mu.Lock()
	defer pl.mu.Unlock()

	for _, pp := range pl.peers {
		if pp.hadTrans && pp.connection == nil && pp.connectable {
			pp.hadTrans = false // consume the flag
			return pp
		}
	}
	return nil
}
