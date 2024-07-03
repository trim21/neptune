package core

import (
	"net/netip"

	"tyr/internal/pkg/empty"
	"tyr/internal/proto"
)

func (d *Download) name() {
	var err error

	d.conn.Range(func(key netip.AddrPort, p *Peer) bool {
		shouldBreak := false
		p.requests.Range(func(req proto.ChunkRequest, _ empty.Empty) bool {
			if !d.bm.Get(req.PieceIndex) {
				return true
			}

			piece, e := d.readPiece(req.PieceIndex)
			if e != nil {
				err = e
				shouldBreak = true
				return false
			}

			p.Response(proto.ChunkResponse{
				Data:       piece[req.Begin : req.Begin+req.Length],
				Begin:      req.Begin,
				PieceIndex: req.PieceIndex,
			})

			return true
		})

		return !shouldBreak
	})

	if err != nil {
		d.setError(err)
	}

}
