//go:build !release

package download

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"time"

	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

type remotePeer struct {
	r            io.Reader
	w            io.Writer
	bm           *bm.Bitmap
	rng          *seededRand
	bufferedReqs []proto.ChunkRequest
	peerID       proto.PeerID
	readBuf      [20]byte
}

func newRemotePeer(rw io.ReadWriter, pieces *bm.Bitmap, peerID proto.PeerID, rng *seededRand) *remotePeer {
	return &remotePeer{r: rw, w: rw, bm: pieces, peerID: peerID, rng: rng}
}

type remoteConfig struct {
	disconnectAfter time.Duration
	chokeAfter      time.Duration
	unchokeAfter    time.Duration
	maxLatency      time.Duration
}

func (rp *remotePeer) Run(cfg remoteConfig, done <-chan struct{}, blockSize int64) error {
	if cfg.maxLatency > 0 {
		rp.randomSleep(cfg.maxLatency)
	}
	if rp.bm.Count() > 0 {
		if err := proto.SendBitfield(rp.w, rp.bm); err != nil {
			return fmt.Errorf("send bitfield: %w", err)
		}
	}
	if err := rp.waitForInterested(); err != nil {
		return fmt.Errorf("wait interested: %w", err)
	}
	if err := proto.SendNoPayload(rp.w, proto.Unchoke); err != nil {
		return fmt.Errorf("send unchoke: %w", err)
	}
	return rp.serveRequests(cfg, blockSize, done)
}

func (rp *remotePeer) randomSleep(max time.Duration) {
	if max <= 0 {
		return
	}
	d := time.Duration(rp.rng.next() % uint64(max))
	if d > 0 {
		time.Sleep(d)
	}
}

func (rp *remotePeer) waitForInterested() error {
	timeout := time.After(3 * time.Second)
	for {
		select {
		case <-timeout:
			return errors.New("timeout waiting for Interested")
		default:
		}
		msgType, payload, err := rp.readMessage()
		if err != nil {
			return err
		}
		switch msgType {
		case byte(proto.Interested):
			return nil
		case byte(proto.NotInterested):
			return nil
		case byte(proto.Request):
			rp.bufferedReqs = append(rp.bufferedReqs, parseRequest(payload))
		}
	}
}

func (rp *remotePeer) serveRequests(cfg remoteConfig, blockSize int64, done <-chan struct{}) error {
	var dcTimer *time.Timer
	var dcChan <-chan time.Time
	if cfg.disconnectAfter > 0 {
		dcTimer = time.NewTimer(cfg.disconnectAfter)
		dcChan = dcTimer.C
		defer dcTimer.Stop()
	}
	var chokeTimer *time.Timer
	var chokeChan <-chan time.Time
	var unchokeTimer *time.Timer
	var unchokeChan <-chan time.Time
	choked := false
	if cfg.chokeAfter > 0 {
		chokeTimer = time.NewTimer(cfg.chokeAfter)
		chokeChan = chokeTimer.C
		defer chokeTimer.Stop()
	}

	zeroBlock := make([]byte, blockSize)
	for _, req := range rp.bufferedReqs {
		if err := rp.sendPiece(req, zeroBlock); err != nil {
			return err
		}
	}
	rp.bufferedReqs = nil

	for {
		select {
		case <-done:
			return nil
		case <-dcChan:
			return nil
		case <-chokeChan:
			if !choked {
				proto.SendNoPayload(rp.w, proto.Choke)
				choked = true
				if cfg.unchokeAfter > 0 {
					unchokeTimer = time.NewTimer(cfg.unchokeAfter)
					unchokeChan = unchokeTimer.C
				}
			}
		case <-unchokeChan:
			if choked {
				proto.SendNoPayload(rp.w, proto.Unchoke)
				choked = false
			}
		default:
		}
		msgType, payload, err := rp.readMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if choked && msgType != byte(proto.Interested) {
			continue
		}
		switch msgType {
		case byte(proto.Request):
			req := parseRequest(payload)
			if cfg.maxLatency > 0 {
				rp.randomSleep(cfg.maxLatency)
			}
			if err := rp.sendPiece(req, zeroBlock); err != nil {
				return err
			}
		}
	}
}

func (rp *remotePeer) readMessage() (byte, []byte, error) {
	if _, err := io.ReadFull(rp.r, rp.readBuf[:4]); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(rp.readBuf[:4])
	if size == 0 {
		return 0, nil, nil
	}
	if size > 1024*1024 {
		return 0, nil, io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(rp.r, payload); err != nil {
		return 0, nil, err
	}
	return payload[0], payload[1:], nil
}

func (rp *remotePeer) sendPiece(req proto.ChunkRequest, zeroBlock []byte) error {
	return proto.SendPiece(rp.w, &proto.ChunkResponse{
		PieceIndex: req.PieceIndex, Begin: req.Begin, Data: zeroBlock[:req.Length],
	})
}

func parseRequest(payload []byte) proto.ChunkRequest {
	return proto.ChunkRequest{
		PieceIndex: binary.BigEndian.Uint32(payload[0:4]),
		Begin:      binary.BigEndian.Uint32(payload[4:8]),
		Length:     binary.BigEndian.Uint32(payload[8:12]),
	}
}

func pipePeer(d *Download, addr netip.AddrPort, pieces *bm.Bitmap, peerID proto.PeerID, rng *seededRand) (*remotePeer, Peer) {
	_ = d.session.ConnSem.Acquire(d.ctx, 1)
	d.session.ConnCount.Add(1)
	local, remote := net.Pipe()
	go func() { _ = proto.SendHandshake(remote, d.info.Hash, peerID, false) }()
	h, err := proto.ReadHandshake(local)
	if err != nil {
		local.Close()
		remote.Close()
		panic("pipePeer: read remote handshake: " + err.Error())
	}
	p := NewIncomingPeer(local, d, addr, h, false)
	var buf [68]byte
	if _, err := io.ReadFull(remote, buf[:]); err != nil {
		local.Close()
		remote.Close()
		panic("pipePeer: read peer handshake: " + err.Error())
	}
	return newRemotePeer(remote, pieces, peerID, rng), p
}
