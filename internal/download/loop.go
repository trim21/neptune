// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"time"

	"neptune/internal/client/tracker"
)

func (d *Download) Start() error {
	if d.completedBm.Count() == d.info.NumPieces {
		if err := d.transition(Seeding); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in Start")
			return err
		}
	} else {
		if err := d.transition(Downloading); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in Start")
			return err
		}
		d.fireStartedHook()
	}

	d.stateCond.Broadcast()
	d.tracker.Resume()
	return nil
}

func (d *Download) Stop() error {
	if err := d.transition(Stopped); err != nil {
		d.log.Error().Err(err).Msg("failed to transition state in Stop")
		return err
	}

	d.stateCond.Broadcast()

	d.tracker.Pause()
	return nil
}

// DemoteToQueued transitions a Downloading torrent to PendingDownloading.
// The download loop skips peer connections in this state,
// but trackers keep running so peers continue to accumulate.
func (d *Download) DemoteToQueued() {
	if err := d.transition(PendingDownloading); err != nil {
		d.log.Error().Err(err).Msg("failed to demote to PendingDownloading")
	}
}

// PromoteFromQueued transitions a PendingDownloading torrent back to Downloading,
// letting the download loop resume peer connections and block requests.
func (d *Download) PromoteFromQueued() {
	if err := d.transition(Downloading); err != nil {
		d.log.Error().Err(err).Msg("failed to promote from PendingDownloading")
	}
}

func (d *Download) AsyncCheck() error {
	if err := d.transition(Checking); err != nil {
		return err
	}

	d.completedBm.Clear()
	d.picker.Load().ResetAll()
	d.completed.Store(0)
	d.stateCond.Broadcast()

	d.runHashCheck(nil)

	return nil
}

// Init check existing files.
func (d *Download) Init(resumed bool, skipHashCheck bool) {
	d.check(resumed, skipHashCheck)

	go d.startBackground()
	d.goBackground(d.tracker.Run)
	d.tracker.Announce(tracker.EventStarted)

	d.saveResume()
}

func (d *Download) wait(state State) bool {
	if d.GetState() != state {
		select {
		case <-d.ctx.Done():
			return false
		case <-d.stateCond.C:
			if d.GetState() != state {
				return false
			}
		}
	}
	return true
}

func (d *Download) startBackground() {
	d.log.Trace().Msg("start goroutine")

	d.goBackground(d.backgroundResHandler)
	d.goBackground(d.backgroundReqHandler)

	// Connection + peer intake loop: handles incoming peers from all sources
	// (tracker, PEX) and periodic connect / turnover.
	d.goBackground(func() {
		defer d.log.Info().Msg("main connection loop: exiting")
		unchokeTicker := time.NewTicker(UnchokeInterval)
		defer unchokeTicker.Stop()

		optimisticTicker := time.NewTicker(30 * time.Second)
		defer optimisticTicker.Stop()

		connectTicker := time.NewTicker(30 * time.Second)
		defer connectTicker.Stop()

		turnoverTicker := time.NewTicker(time.Minute)
		defer turnoverTicker.Stop()

		for {
			select {
			case <-d.ctx.Done():
				d.log.Info().Msg("main connection loop: exiting (ctx canceled)")
				return

			case <-unchokeTicker.C:
				d.recalculateUnchokeSlots()
				d.recalcPeerCounts()
				continue
			case <-optimisticTicker.C:
				if d.IsActiveDownloading() {
					d.optimisticUnchoke()
				}
				continue

			case peers := <-d.peersCh:
				for _, p := range peers {
					d.peerList.addPeer(p.AddrPort, p.Source, true)
				}

			case <-d.pendingPeersSignal:
			case <-connectTicker.C:
			case <-turnoverTicker.C:
				d.peerTurnover()
				continue
			}

			if !d.IsActive() {
				continue
			}

			desired := d.maxConnections()
			current := d.peerCount()
			maxSlots := desired - current
			if maxSlots <= 0 {
				continue
			}

			d.connectToPeers(maxSlots)
		}
	})
}

func (d *Download) goBackground(fn func()) {
	d.backgroundWg.Go(func() {
		fn()
	})
}

func (d *Download) optimisticUnchoke() {
	var peers []Peer
	d.peers.Range(func(_ uint64, p Peer) bool {
		if !p.Closed() && !p.IsSnubbed() {
			peers = append(peers, p)
		}
		return true
	})

	if len(peers) == 0 {
		return
	}

	idx := int(time.Now().UnixNano()) % len(peers)
	p := peers[idx]
	d.log.Debug().Stringer("addr", p.Addr()).Msg("optimistic unchoke")
}

type Priority struct {
	Index  uint32
	Weight uint32
}

type PriorityQueue []Priority

func (p *PriorityQueue) Len() int {
	return len(*p)
}

func (p *PriorityQueue) Less(i, j int) bool {
	return (*p)[i].Weight > (*p)[j].Weight
}

func (p *PriorityQueue) Swap(i, j int) {
	(*p)[i], (*p)[j] = (*p)[j], (*p)[i]
}

func (p *PriorityQueue) Push(item Priority) {
	*p = append(*p, item)
}

func (p *PriorityQueue) Pop() Priority {
	old := *p
	n := len(old)
	x := old[n-1]
	*p = old[:n-1]
	return x
}

// maxConnections returns the per-torrent connection limit.
func (d *Download) maxConnections() int {
	return int(d.session.TorrentConnLimit.Load())
}

// peerCount returns the number of currently connected peers.
func (d *Download) peerCount() int {
	return d.peers.Size()
}
