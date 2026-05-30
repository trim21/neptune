// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/go-units"
	"github.com/dustin/go-humanize"

	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/filepool"
)

const defaultBlockSize = units.KiB * 16

func (d *Download) Start() {
	if d.bm.Count() == d.info.NumPieces {
		d.state.Store(uint32(Seeding))
	} else {
		d.state.Store(uint32(Downloading))
	}

	d.stateCond.Broadcast()
}

func (d *Download) Stop() {
	d.state.Store(uint32(Stopped))

	d.stateCond.Broadcast()

	d.announce(EventStopped)
}

func (d *Download) Check() {
	d.state.Store(uint32(Checking))
	d.bm.Clear()

	d.stateCond.Broadcast()
}

// Init check existing files.
func (d *Download) Init(resumed bool) {
	if !resumed {
		d.log.Debug().Msg("initializing download")

		d.state.Store(uint32(Checking))

		err := d.initCheck()
		if err != nil {
			d.setError(err)
			d.log.Err(err).Msg("failed to initCheck torrent data")
		}
		// unsafe methods are safe here because d hasn't been shared with other goroutines yet.
		d.markUnselectedPiecesDoneUnsafe()
		d.completed.Store(d.computeCompletedUnsafe())
		d.ioDown.Reset()

		d.log.Debug().Msgf("done size %s", humanize.IBytes(uint64(d.bm.Count())*uint64(d.info.PieceLength)))

		if d.bm.Count() == d.info.NumPieces {
			d.state.Store(uint32(Seeding))
		} else {
			d.state.Store(uint32(Downloading))
		}
	}

	go d.startBackground()

	go func() {
		d.announce(EventStarted)
		for {
			// download removed from application, stop goroutine
			if d.ctx.Err() != nil {
				return
			}
			time.Sleep(time.Second * 5)
			d.TryAnnounce()
		}
	}()

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

func (d *Download) handleConnectionChange() {
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.buildNetworkPieces:
			// d.onConnectionChanged()
		}
	}
}

func (d *Download) startBackground() {
	d.log.Trace().Msg("start goroutine")

	// download
	go d.backgroundResHandler()
	go d.backgroundReqScheduler()
	go d.handleConnectionChange()

	// upload
	go d.backgroundReqHandler()
	go d.unchokeLoop()

	go d.backgroundTrackerHandler()

	go func() {
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-d.pendingPeersSignal:
				if !d.wait(Seeding | Downloading) {
					continue
				}

				d.connectToPeers()
			}
		}
	}()

	go func() {
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-d.pexDrop:
			case peers := <-d.pexAdd:
				state := d.GetState()

				d.pendingPeersMutex.Lock()

				for _, peer := range peers {
					if !peer.outGoing {
						continue
					}

					if state == Seeding && peer.seedOnly {
						continue
					}

					d.pendingPeers.Push(peerWithPriority{
						addrPort: peer.addrPort,
						priority: d.c.PeerPriority(peer.addrPort),
					})
				}

				d.pendingPeersMutex.Unlock()

				d.pendingPeersSignal <- empty.Empty{}
			}
		}
	}()

	// optimistic unchoke: periodically reset a random peer's Requested bitmap
	// to discover new fast peers and prevent stale assignments
	go d.optimisticUnchokeLoop()
}

// optimisticUnchokeLoop periodically picks a random peer and clears its Requested
// bitmap, giving it a chance to receive new piece assignments from the scheduler.
// This helps discover fast peers that may have been overlooked.
func (d *Download) optimisticUnchokeLoop() {
	const interval = 30 * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-timer.C:
			timer.Reset(interval)

			if !d.wait(Downloading | Seeding) {
				continue
			}

			// collect all peers
			var peers []*Peer
			d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
				if !p.closed.Load() && !p.snubbed.Load() {
					peers = append(peers, p)
				}
				return true
			})

			if len(peers) == 0 {
				continue
			}

			// pick a random peer
			idx := int(time.Now().UnixNano()) % len(peers)
			p := peers[idx]
			p.Requested.Clear()
			d.log.Debug().Stringer("addr", p.Address).Msg("optimistic unchoke: cleared peer Requested")

			// trigger reschedule
			select {
			case d.scheduleRequestSignal <- empty.Empty{}:
			default:
			}
		}
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

func (d *Download) openFile(fileIndex int) (*filepool.File, error) {
	d.m.RLock()
	p := filepath.Join(d.basePath, d.info.Files[fileIndex].Path)
	d.m.RUnlock()

	file, err := d.c.filePool.Open(p, os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour)
	if err == nil {
		return file, nil
	}

	if os.IsNotExist(err) {
		// only try to create directory if needed.
		err := os.MkdirAll(filepath.Dir(p), os.ModePerm)
		if err != nil {
			return nil, err
		}
	}

	return d.c.filePool.Open(p, os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour)
}
