// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/go-units"

	"neptune/internal/core/tracker"
	"neptune/internal/pkg/empty"
	"neptune/internal/pkg/fadvise"
	"neptune/internal/pkg/filepool"
)

const defaultBlockSize = units.KiB * 16

func (d *Download) Start() error {
	if d.bm.Count() == d.info.NumPieces {
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

	d.bm.Clear()
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

		d.markUnselectedPiecesDoneUnsafe()
		d.completed.Store(d.computeCompletedUnsafe())
		d.ioDown.Reset()

		if d.bm.Count() == d.info.NumPieces {
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

	d.Trk.Start(d.ctx)
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

	d.goBackground(func() {
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
	})

	d.goBackground(func() {
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
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
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
	p.Requested.Clear()
	d.log.Debug().Stringer("addr", p.Address).Msg("optimistic unchoke: cleared peer Requested")

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

func (d *Download) openFile(fileIndex int) (*filepool.File, error) {
	d.m.RLock()
	p := filepath.Join(d.basePath, d.info.Files[fileIndex].Path)
	d.m.RUnlock()

	file, fresh, err := d.c.filePool.Open(p, os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour)
	if err == nil {
		d.adviseFresh(file, fresh)
		return file, nil
	}

	if os.IsNotExist(err) {
		// only try to create directory if needed.
		err = os.MkdirAll(filepath.Dir(p), os.ModePerm)
		if err != nil {
			return nil, err
		}
	}

	file, fresh, err = d.c.filePool.Open(p, os.O_RDWR|os.O_CREATE, os.ModePerm, time.Hour)
	if err != nil {
		return nil, err
	}
	d.adviseFresh(file, fresh)
	return file, nil
}

// adviseFresh sets FADV_RANDOM on newly-opened fds. Piece access during
// download/seed is random; initCheck handles its own Sequential advice.
func (d *Download) adviseFresh(f *filepool.File, fresh bool) {
	if fresh {
		_ = fadvise.Random(f.File, 0, 0)
	}
}

func (d *Download) openFileReadOnly(fileIndex int) (*filepool.File, error) {
	d.m.RLock()
	p := filepath.Join(d.basePath, d.info.Files[fileIndex].Path)
	d.m.RUnlock()

	file, fresh, err := d.c.filePool.Open(p, os.O_RDONLY, 0, time.Hour)
	if err != nil {
		return nil, err
	}
	d.adviseFresh(file, fresh)
	return file, nil
}
