// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"time"

	"github.com/docker/go-units"

	"neptune/internal/client/tracker"
	"neptune/internal/pkg/empty"
)

const defaultBlockSize = units.KiB * 16

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
	}

	d.stateCond.Broadcast()
	d.Trk.Resume()
	return nil
}

func (d *Download) Stop() error {
	if err := d.transition(Stopped); err != nil {
		d.log.Error().Err(err).Msg("failed to transition state in Stop")
		return err
	}

	d.stateCond.Broadcast()

	d.Trk.Pause()
	return nil
}

func (d *Download) AsyncCheck() error {
	if err := d.transition(Checking); err != nil {
		return err
	}

	d.completedBm.Clear()
	d.picker.resetAll()
	d.completed.Store(0)
	d.stateCond.Broadcast()

	go func() {
		if err := d.initCheck(); err != nil {
			if d.ctx.Err() != nil {
				return
			}
			d.setError(err)
			d.log.Err(err).Msg("failed to recheck torrent data")
			return
		}

		d.s.mu.RLock()
		d.markUnselectedPiecesDoneUnsafe()
		d.completed.Store(d.computeCompletedUnsafe())
		d.s.mu.RUnlock()
		d.pieceDownloadRate.Reset()

		if d.completedBm.Count() == d.info.NumPieces {
			if err := d.transition(Seeding); err != nil {
				d.log.Error().Err(err).Msg("failed to transition state after recheck")
				return
			}
		} else {
			if err := d.transition(Downloading); err != nil {
				d.log.Error().Err(err).Msg("failed to transition state after recheck")
				return
			}
		}
		d.stateCond.Broadcast()
	}()

	return nil
}

// Init check existing files.
func (d *Download) Init(resumed bool, skipHashCheck bool) {
	d.check(resumed, skipHashCheck)

	d.goBackground(d.Trk.Run)
	d.Trk.Announce(tracker.EventStarted)
	go d.startBackground()

	d.saveResume()
}

func (d *Download) wait(states State) bool {
	if !d.HasState(states) {
		select {
		case <-d.ctx.Done():
			return false
		case <-d.stateCond.C:
			if !d.HasState(states) {
				return false
			}
		}
	}

	return true
}

func (d *Download) startBackground() {
	d.log.Trace().Msg("start goroutine")

	d.goBackground(d.backgroundResHandler)
	d.goBackground(d.backgroundReqScheduler)
	d.goBackground(d.backgroundReqHandler)

	// Connection loop — libtorrent-style periodic connect attempts.
	// 1s ticker (matching libtorrent's tick_interval) for prompt reuse of freed slots.
	// 5min ticker for peer turnover (disconnect slow peers, connect new candidates).
	d.goBackground(func() {
		connectTicker := time.NewTicker(time.Second)
		defer connectTicker.Stop()

		turnoverTicker := time.NewTicker(5 * time.Minute)
		defer turnoverTicker.Stop()

		for {
			select {
			case <-d.ctx.Done():
				return
			case <-d.pendingPeersSignal:
			case <-connectTicker.C:
			case <-turnoverTicker.C:
				d.peerTurnover()
				continue
			}

			if !d.wait(Seeding | Downloading) {
				continue
			}

			// Compute how many slots we can fill
			desired := d.maxConnections()
			current := d.peerCount()
			maxSlots := desired - current
			if maxSlots <= 0 {
				continue
			}

			d.connectToPeers(maxSlots)
		}
	})

	// PEX handler — feeds peers from PEX messages into the persistent peer list.
	d.goBackground(func() {
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-d.pexDrop:
			case peers := <-d.pexAdd:
				state := d.GetState()

				for _, peer := range peers {
					if !peer.outGoing {
						continue
					}
					if state == Seeding && peer.seedOnly {
						continue
					}

					d.peerList.addPeer(peer.addrPort, peerSourcePEX, true)
				}

				select {
				case d.pendingPeersSignal <- empty.Empty{}:
				case <-d.ctx.Done():
				}
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
	var peers []*Peer
	d.peers.Range(func(_ uint64, p *Peer) bool {
		if !p.closed.Load() && !p.snubbed.Load() {
			peers = append(peers, p)
		}
		return true
	})

	if len(peers) == 0 {
		return
	}

	idx := int(time.Now().UnixNano()) % len(peers)
	p := peers[idx]
	d.log.Debug().Stringer("addr", p.Address).Msg("optimistic unchoke")

	select {
	case d.scheduleRequestSignal <- empty.Empty{}:
	default:
	}
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
	return int(d.session.Config.App.GlobalConnectionLimit)
}

// peerCount returns the number of currently connected peers.
func (d *Download) peerCount() int {
	return d.peers.Size()
}
