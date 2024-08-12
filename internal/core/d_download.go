// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"cmp"
	"crypto/sha1"
	"net/netip"
	"slices"
	"time"

	"github.com/docker/go-units"
	"github.com/kelindar/bitmap"
	"github.com/trim21/errgo"

	"neptune/internal/meta"
	"neptune/internal/pkg/as"
	"neptune/internal/pkg/bm"
	"neptune/internal/pkg/global/tasks"
	"neptune/internal/pkg/gslice"
	"neptune/internal/pkg/gsync"
	"neptune/internal/pkg/heap"
	"neptune/internal/pkg/mempool"
	"neptune/internal/proto"
)

func (d *Download) backgroundReqScheduler() {
	timer := time.NewTimer(time.Second / 5)
	defer timer.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.scheduleRequestSignal:
			if !d.wait(Downloading) {
				continue
			}

			d.scheduleSeq()
		case <-timer.C:
			if !d.wait(Downloading) {
				continue
			}

			d.scheduleSeq()
		}
	}
}

func (d *Download) have(index uint32) {
	tasks.Submit(func() {
		d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
			p.Have(index)
			return true
		})
	})
}

type responseChunk struct {
	res *proto.ChunkResponse
	pi  uint32
}

func (r responseChunk) Less(o responseChunk) bool {
	return r.pi < o.pi
}

func (d *Download) backgroundResHandler() {
	d.chunkHeap = heap.Heap[responseChunk]{}
	for {
		select {
		case <-d.ctx.Done():
			return
		case res := <-d.ResChan:
			if d.GetState() != Downloading {
				continue
			}

			d.handleRes(res)
		}
	}
}

const defaultChunkHeapSizeLimit = 1000

var pieceChunksPool = gsync.NewPool(func() *mempool.Buffer {
	return &mempool.Buffer{
		B: make([]byte, defaultBlockSize*10),
	}
})

func (d *Download) handleRes(res *proto.ChunkResponse) {
	d.log.Trace().
		Int("length", len(res.Data)).
		Uint32("offset", res.Begin).
		Uint32("piece", res.PieceIndex).
		Msg("res received")

	d.ioDown.Update(len(res.Data))
	d.netDown.Update(len(res.Data))
	d.c.ioDown.Update(len(res.Data))
	d.downloaded.Add(int64(len(res.Data)))

	// in endgame mode we may receive duplicated response, just ignore them
	if d.bm.Contains(res.PieceIndex) {
		return
	}

	if d.endGameMode.Load() {
		d.handleResEndgame(res)
		return
	}

	c := responseChunk{
		res: res,
		pi:  res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen,
	}

	d.chunkHeap.Push(c)
	d.pendingChunksMap.Set(c.pi)

	if d.chunkHeap.Len() < defaultChunkHeapSizeLimit {
		piecePiStart := res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen
		piecePiEnd := piecePiStart + uint32(pieceChunksCount(d.info, res.PieceIndex))
		for i := piecePiStart; i < piecePiEnd; i++ {
			if !d.pendingChunksMap.Contains(c.pi) {
				return
			}
		}
		d.handlePieceFromHeap(res.PieceIndex)
		return
	}

	head := d.chunkHeap.Pop()
	headPi := head.pi

	mergedChunk := pieceChunksPool.Get()
	defer pieceChunksPool.Put(mergedChunk)
	mergedChunk.Reset()

	d.chunkMap.Set(headPi)

	headPiece := head.res.PieceIndex
	tailPiece := headPiece
	tailPi := headPi

	start := int64(headPiece)*d.info.PieceLength + int64(head.res.Begin)

	mergedChunk.Write(head.res.Data)

	proto.PiecePool.Put(head.res)

	for d.chunkHeap.Len() != 0 {
		peak := d.chunkHeap.Peek()
		if tailPi+1 != peak.pi {
			break
		}

		if tailPi-headPi >= 10 {
			break
		}

		tailPi++
		tailPiece = peak.res.PieceIndex

		d.chunkMap.Set(tailPi)

		mergedChunk.Write(peak.res.Data)

		d.chunkHeap.Pop()
		proto.PiecePool.Put(peak.res)
	}

	err := d.writeChunkToDist(start, mergedChunk.B)
	if err != nil {
		return
	}

	for pieceIndex := headPiece; pieceIndex <= tailPiece; pieceIndex++ {
		if !d.checkPieceBitmapDone(pieceIndex) {
			continue
		}

		tasks.Submit(func() {
			err := d.checkPiece(pieceIndex)
			if err != nil {
				return
			}

			d.checkDone()
		})
	}
}

func (d *Download) handleResEndgame(res *proto.ChunkResponse) {
	d.chunkHeap.Push(responseChunk{
		res: res,
		pi:  res.Begin/defaultBlockSize + res.PieceIndex*d.normalChunkLen,
	})

	for d.chunkHeap.Len() != 0 {
		chunk := d.chunkHeap.Pop()
		index := chunk.res.PieceIndex
		err := d.writeChunkToDist(int64(index)*d.info.PieceLength+int64(chunk.res.Begin), chunk.res.Data)
		d.chunkMap.Set(chunk.pi)
		proto.PiecePool.Put(chunk.res)

		if err != nil {
			continue
		}

		if d.checkPieceBitmapDone(index) {
			tasks.Submit(func() {
				err := d.checkPiece(index)
				if err != nil {
					return
				}

				d.checkDone()
			})
		}
	}
}

// find all chunks from chunkHeap and write them to disk.
func (d *Download) handlePieceFromHeap(index uint32) {
	chunks := heap.New[responseChunk]()
	for _, chunk := range d.chunkHeap.Data {
		if chunk.res.PieceIndex == index {
			chunks.Push(chunk)
		}
	}

	if chunks.Len() != int(pieceChunksCount(d.info, index)) {
		return
	}

	for _, chunk := range chunks.Data {
		d.chunkHeap.Data = gslice.Remove(d.chunkHeap.Data, chunk)
	}

	buf := mempool.GetWithCap(int(d.pieceLength(index)))
	defer mempool.Put(buf)
	buf.Reset()

	for chunks.Len() != 0 {
		chunk := chunks.Pop()
		buf.Write(chunk.res.Data)
		d.chunkMap.Set(chunk.pi)
		proto.PiecePool.Put(chunk.res)
	}

	err := d.writeChunkToDist(int64(index)*d.info.PieceLength, buf.B)
	if err != nil {
		return
	}

	tasks.Submit(func() {
		err := d.checkPiece(index)
		if err != nil {
			return
		}

		d.checkDone()
	})
}

func (d *Download) checkPieceBitmapDone(index uint32) bool {
	pieceCidStart := index * d.normalChunkLen
	pieceCidEnd := pieceCidStart + uint32(pieceChunksCount(d.info, index))

	for i := pieceCidStart; i < pieceCidEnd; i++ {
		if !d.chunkMap.Contains(i) {
			return false
		}
	}

	return true
}

func (d *Download) writeChunkToDist(begin int64, data []byte) error {
	size := int64(len(data))

	var offset int64
	for _, chunk := range fileChunks(d.info, begin, begin+size) {
		f, err := d.openFile(chunk.fileIndex)
		if err != nil {
			d.setError(err)
			return errgo.Wrap(err, "failed to open file for writing chunk")
		}

		_, err = f.File.WriteAt(data[offset:offset+chunk.length], chunk.offsetOfFile)
		if err != nil {
			f.Close()
			d.setError(err)
			return errgo.Wrap(err, "failed to write chunk")
		}

		f.Release()
		offset += chunk.length
	}

	return nil
}

func (d *Download) checkPiece(pieceIndex uint32) error {
	// TODO(perf): there are some torrents have piece size 256mib, and we should not read them into memory in one shot

	size := d.pieceLength(pieceIndex)
	buf := mempool.GetWithCap(int(size))
	defer mempool.Put(buf)

	err := d.readPiece(pieceIndex, buf.B)
	if err != nil {
		return errgo.Wrap(err, "failed to read piece")
	}

	if sha1.Sum(buf.B) != d.info.Pieces[pieceIndex] {
		d.corrupted.Add(d.info.PieceLength)
		for i := pieceIndex * d.normalChunkLen; i < uint32(pieceChunksCount(d.info, pieceIndex)); i++ {
			d.chunkMap.Remove(i)
		}
		return nil
	}

	notHave := d.bm.SetX(pieceIndex)

	if notHave {
		d.completed.Add(size)
		d.log.Trace().Msgf("piece %d done", pieceIndex)
		d.have(pieceIndex)
	}

	return nil
}

func (d *Download) checkDone() {
	if d.bm.Count() != d.info.NumPieces {
		return
	}

	d.m.Lock()
	d.state = Seeding
	d.ioDown.Reset()
	d.m.Unlock()

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		if p.Bitmap.Count() == d.info.NumPieces {
			p.close()
		}

		return true
	})

	d.announce(EventCompleted)
}

func (d *Download) updateRarePieces() {
	d.ratePieceMutex.Lock()
	defer d.ratePieceMutex.Unlock()

	if len(d.pieceRare) == 0 {
		d.pieceRare = make([]uint32, d.info.NumPieces)
	} else {
		clear(d.pieceRare)
	}

	var requested = make(bitmap.Bitmap, d.bitfieldSize)

	var baseRare uint32
	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		requested.Or(p.Requested)
		if p.peerChoking.Load() {
			p.allowFast.Range(func(u uint32) {
				if p.Bitmap.Contains(u) {
					d.pieceRare[u]++
				}
			})
			return true
		}

		// doesn't contribute to rare
		if p.Bitmap.Count() == d.info.NumPieces {
			baseRare++
			return true
		}

		p.Bitmap.Range(func(u uint32) {
			d.pieceRare[u]++
		})

		return true
	})

	var queue = make([]pieceRare, 0, d.info.NumPieces)

	for index, rare := range d.pieceRare {
		if d.bm.Contains(uint32(index)) {
			continue
		}

		if requested.Contains(uint32(index)) {
			continue
		}

		if rare+baseRare == 0 {
			continue
		}

		queue = append(queue, pieceRare{rare: rare + baseRare, index: uint32(index)})
	}

	d.rarePieceQueue = heap.FromSlice(queue)
}

func (d *Download) scheduleSeq() {
	if d.endGameMode.Load() {
		d.scheduleSeqEndGame()
		return
	}

	if d.info.TotalLength-d.completed.Load() <= units.MiB*100 {
		d.endGameMode.Store(true)
		d.scheduleSeqEndGame()
		return
	}

	d.updateRarePieces()
	var peers = make([]*Peer, 0, d.peers.Size())

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		peers = append(peers, p)
		return true
	})

	slices.SortFunc(peers, func(a, b *Peer) int {
		return cmp.Compare(a.ioIn.Status().CurRate, b.ioIn.Status().CurRate)
	})

	d.ratePieceMutex.Lock()
	q := d.rarePieceQueue
	d.rarePieceQueue = heap.New[pieceRare]()
	d.ratePieceMutex.Unlock()

PIECE:
	for q.Len() > 0 {
		piece := q.Pop()

		if piece.rare == 0 {
			return
		}

		for _, p := range peers {
			if p.closed.Load() {
				// peer closed, re-scheduler
				return
			}

			if p.peerChoking.Load() {
				if !p.allowFast.Contains(piece.index) {
					continue
				}

				select {
				case p.ourPieceRequests <- piece.index:
					p.Requested.Set(piece.index)
				default:
				}

				continue
			}

			if !p.Bitmap.Contains(piece.index) {
				continue
			}

			select {
			case p.ourPieceRequests <- piece.index:
				p.Requested.Set(piece.index)
				continue PIECE
			default:
			}
		}
	}
}

func (d *Download) scheduleSeqEndGame() {
	missing := bm.New(d.info.NumPieces)
	missing.Fill()
	missing.AndNot(d.bm)
	s := missing.ToArray()

	d.peers.Range(func(addr netip.AddrPort, p *Peer) bool {
		for _, u := range s {
			if p.Bitmap.Contains(u) {
				select {
				case <-p.ctx.Done():
					return true
				case p.ourPieceRequests <- u:
				default:
				}
			}
		}

		return true
	})
}

func pieceChunksCount(info meta.Info, index uint32) int64 {
	pieceSize := info.PieceLength
	if index == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}

	return (pieceSize + defaultBlockSize - 1) / defaultBlockSize
}

func pieceChunk(info meta.Info, index uint32, chunkIndex int) proto.ChunkRequest {
	pieceSize := info.PieceLength
	if index == info.NumPieces-1 {
		pieceSize = info.LastPieceSize
	}

	begin := defaultBlockSize * int64(chunkIndex)
	end := min(begin+defaultBlockSize, pieceSize)

	return proto.ChunkRequest{
		PieceIndex: index,
		Begin:      uint32(begin),
		Length:     as.Uint32(end - begin),
	}
}
