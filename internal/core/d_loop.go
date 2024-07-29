// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
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
	d.m.Lock()
	if d.bm.Count() == d.info.NumPieces {
		d.state = Seeding
	} else {
		d.state = Downloading
	}
	d.m.Unlock()

	d.stateCond.Broadcast()
}

func (d *Download) Stop() {
	d.m.Lock()
	d.state = Stopped
	d.m.Unlock()

	d.stateCond.Broadcast()

	d.announce(EventStopped)
}

func (d *Download) Check() {
	d.m.Lock()
	d.state = Checking
	d.bm.Clear()
	d.m.Unlock()

	d.stateCond.Broadcast()
}

// Init check existing files
func (d *Download) Init() {
	d.log.Debug().Msg("initializing download")

	d.m.Lock()
	d.state = Checking
	d.m.Unlock()

	err := d.initCheck()
	if err != nil {
		d.setError(err)
		d.log.Err(err).Msg("failed to initCheck torrent data")
	}

	d.ioDown.Reset()

	d.log.Debug().Msgf("done size %s", humanize.IBytes(uint64(d.bm.Count())*uint64(d.info.PieceLength)))

	d.m.Lock()
	if d.bm.Count() == d.info.NumPieces {
		d.state = Seeding
	} else {
		d.state = Downloading
	}
	d.m.Unlock()

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
}

func (d *Download) wait(state State) bool {
	if d.GetState()|state == 0 {
		select {
		case <-d.ctx.Done():
			return false
		case <-d.stateCond.C:
			if d.GetState()|state == 0 {
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
			//d.onConnectionChanged()
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
	p := filepath.Join(d.basePath, d.info.Files[fileIndex].Path)

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
