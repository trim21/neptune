// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"context"
	"slices"

	"neptune/internal/proto"
)

type uploadTask struct {
	d    *Download
	peer *Peer
	req  proto.ChunkRequest
}

func (c *Client) startUploadPool() {
	workerCount := int(c.Config.App.GlobalUploadSlots)
	if workerCount <= 0 {
		workerCount = 64
	}
	// Avoid spawning an extreme number of goroutines due to misconfiguration.
	workerCount = min(workerCount, 4096)

	// Bounded queue to avoid unbounded memory growth under request storms.
	// Keep it large enough to absorb bursts while still bounded.
	queueCap := workerCount * 256
	queueCap = min(queueCap, 65536)
	queueCap = max(queueCap, 1024)

	c.uploadQ = make(chan uploadTask, queueCap)

	for i := 0; i < workerCount; i++ {
		go c.uploadWorker()
	}
}

func (c *Client) tryEnqueueUpload(t uploadTask) bool {
	select {
	case c.uploadQ <- t:
		return true
	default:
		return false
	}
}

func (c *Client) uploadWorker() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case t := <-c.uploadQ:
			d := t.d
			p := t.peer

			if d == nil || p == nil {
				continue
			}
			if d.ctx.Err() != nil || p.closed.Load() {
				continue
			}
			if d.GetState()&(Downloading|Seeding) == 0 {
				continue
			}

			res := proto.PiecePool.Get()
			res.PieceIndex = t.req.PieceIndex
			res.Begin = t.req.Begin
			res.Data = slices.Grow(res.Data[:0], int(t.req.Length))[:t.req.Length]

			err := d.readPieceRangeCtx(d.ctx, t.req, res.Data)
			if err != nil {
				proto.PiecePool.Put(res)
				// Do not treat pause/cancel as a torrent error.
				if err == errUploadPaused || err == context.Canceled {
					continue
				}
				d.setError(err)
				p.close()
				continue
			}

			if p.Response(res) {
				d.ioUp.Update(len(res.Data))
				d.c.ioUp.Update(len(res.Data))
				d.uploaded.Add(int64(len(res.Data)))
			}
			proto.PiecePool.Put(res)
		}
	}
}
