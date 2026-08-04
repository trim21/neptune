// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"time"
)

func (d *Download) Start() error {
	if d.isComplete() {
		if _, err := d.transition(Seeding); err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in Start")
			return err
		}
	} else {
		transition, err := d.transition(Downloading)
		if err != nil {
			d.log.Error().Err(err).Msg("failed to transition state in Start")
			return err
		}
		if transition.changed {
			d.fireStartedHook()
		}
	}

	d.stateCond.Broadcast()
	// A restarted download may have accumulated candidates while stopped.
	d.signalConnect()
	return nil
}

func (d *Download) Stop() error {
	if d.HasState(Moving) {
		d.CancelMove()
	}
	if _, err := d.transition(Stopped); err != nil {
		d.log.Error().Err(err).Msg("failed to transition state in Stop")
		return err
	}
	d.CancelMove()

	d.stateCond.Broadcast()
	return nil
}

// DemoteToQueued transitions a Downloading torrent to PendingDownloading.
// The download loop skips peer connections in this state,
// but trackers keep running so peers continue to accumulate.
func (d *Download) DemoteToQueued() {
	if _, err := d.transition(PendingDownloading); err != nil {
		d.log.Error().Err(err).Msg("failed to demote to PendingDownloading")
		return
	}
	// Flush responses already accepted before the demotion. New in-flight
	// responses remain valid and are flushed as they arrive.
	d.stateCond.Broadcast()
}

// PromoteFromQueued transitions a PendingDownloading torrent back to Downloading,
// letting the download loop resume peer connections and block requests.
func (d *Download) PromoteFromQueued() {
	if _, err := d.transition(Downloading); err != nil {
		d.log.Error().Err(err).Msg("failed to promote from PendingDownloading")
		return
	}

	// Bitfield/Have/Unchoke events received while queued do not schedule block
	// requests. Wake existing peers after promotion so they can immediately fill
	// their request queues instead of waiting for another protocol event.
	d.stateCond.Broadcast()
	d.notifyPeersToRequest()

	// Also wake the connection scheduler so a promoted download does not have
	// to wait for the next peer-intake event.
	d.signalConnect()
}

func (d *Download) AsyncCheck() error {
	if _, err := d.transition(Checking); err != nil {
		return err
	}

	d.completedBm.Clear()
	d.picker.Load().ResetAll()
	d.completed.Store(0)
	d.stateCond.Broadcast()

	d.runHashCheck(nil)

	return nil
}

func (d *Download) startRuntime() {
	d.startBackground()
	d.goBackground(d.tracker.Run)
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

	d.goBackground(d.connectLoop)
	d.goBackground(d.backgroundResHandler)
	d.goBackground(d.backgroundReqHandler)
	d.startPeerIntake()

	// Background housekeeping loop: unchoke recalculation, optimistic unchoke
	// and peer turnover. Peer connection dispatch runs in its own connectLoop.
	d.goBackground(func() {
		defer d.log.Info().Msg("main connection loop: exiting")
		unchokeTicker := time.NewTicker(UnchokeInterval)
		defer unchokeTicker.Stop()

		optimisticTicker := time.NewTicker(30 * time.Second)
		defer optimisticTicker.Stop()

		turnoverTicker := time.NewTicker(5 * time.Minute)
		defer turnoverTicker.Stop()

		for {
			select {
			case <-d.ctx.Done():
				d.log.Info().Msg("main connection loop: exiting (ctx canceled)")
				return

			case <-unchokeTicker.C:
				d.recalculateUnchokeSlots()
				d.recalcPeerCounts()
			case <-optimisticTicker.C:
				if d.IsActiveDownloading() {
					d.optimisticUnchoke()
				}
			case <-turnoverTicker.C:
				d.peerTurnover()
			}
		}
	})
}

// startPeerIntake consumes discovered peers (tracker announce, PEX) and adds
// them to the peer list, then wakes the connection loop. Running in its own
// goroutine decouples peer injection from connection dispatch: a loop blocked
// on DialSem can never block tracker announces.
func (d *Download) startPeerIntake() {
	d.goBackground(func() {
		for {
			select {
			case <-d.ctx.Done():
				return
			case peers := <-d.peersCh:
				for _, p := range peers {
					d.peerList.addPeer(p.AddrPort, p.Source)
				}
				d.signalConnect()
			}
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
	d.peerList.Range(func(_ uint64, p Peer) bool {
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
	return d.peerList.Size()
}
