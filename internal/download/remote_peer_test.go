// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package download

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"time"

	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

type remotePeer struct {
	r            io.ReadCloser
	w            io.Writer
	bm           *bm.Bitmap
	rng          *rand.Rand
	bufferedReqs []proto.ChunkRequest
	peerID       proto.PeerID
	readBuf      [20]byte
}

func newRemotePeer(rw io.ReadWriteCloser, pieces *bm.Bitmap, peerID proto.PeerID, rng *rand.Rand) *remotePeer {
	return &remotePeer{r: rw, w: rw, bm: pieces, peerID: peerID, rng: rng}
}

func (rp *remotePeer) Close() error { return rp.r.Close() }

type remoteConfig struct {
	disconnectAfter         time.Duration
	chokeAfter              time.Duration
	unchokeAfter            time.Duration
	maxLatency              time.Duration
	disconnectAfterRequests uint32
	rejectEvery             uint32
}

type remoteMessage struct {
	payload     []byte
	messageType byte
}

func (rp *remotePeer) Run(cfg remoteConfig, done <-chan struct{}, blockSize int64) error {
	defer rp.Close()
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

func (rp *remotePeer) randomSleep(maxValue time.Duration) {
	if maxValue <= 0 {
		return
	}
	d := time.Duration(rp.rng.Uint64() % uint64(maxValue))
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

	// The local peer may pipeline requests while this goroutine is blocked
	// writing a Piece response. Buffer decoded messages to avoid an artificial
	// full-duplex deadlock caused by net.Pipe's zero-capacity writes.
	messages := make(chan remoteMessage, maxRequestQueue)
	readErr := make(chan error, 1)
	go func() {
		for {
			msgType, payload, err := rp.readMessage()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case messages <- remoteMessage{messageType: msgType, payload: payload}:
			case <-done:
				return
			}
		}
	}()

	zeroBlock := make([]byte, blockSize)
	requestsSeen := uint32(0)
	handleRequest := func(req proto.ChunkRequest) (bool, error) {
		requestsSeen++
		if cfg.disconnectAfterRequests > 0 && requestsSeen > cfg.disconnectAfterRequests {
			return true, nil
		}
		if cfg.maxLatency > 0 {
			rp.randomSleep(cfg.maxLatency)
		}
		if cfg.rejectEvery > 0 && requestsSeen%cfg.rejectEvery == 0 {
			return false, proto.SendReject(rp.w, req)
		}
		return false, rp.sendPiece(req, zeroBlock)
	}

	for _, req := range rp.bufferedReqs {
		disconnect, err := handleRequest(req)
		if err != nil || disconnect {
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
				if err := proto.SendNoPayload(rp.w, proto.Choke); err != nil {
					return err
				}
				choked = true
				chokeChan = nil
				if cfg.unchokeAfter > 0 {
					unchokeTimer = time.NewTimer(cfg.unchokeAfter)
					unchokeChan = unchokeTimer.C
				}
			}
		case <-unchokeChan:
			if choked {
				if err := proto.SendNoPayload(rp.w, proto.Unchoke); err != nil {
					return err
				}
				choked = false
				unchokeChan = nil
			}
		case err := <-readErr:
			if err == io.EOF || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		case msg := <-messages:
			if choked && msg.messageType != byte(proto.Interested) {
				continue
			}
			if msg.messageType != byte(proto.Request) {
				continue
			}
			disconnect, err := handleRequest(parseRequest(msg.payload))
			if err != nil || disconnect {
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

func pipePeer(d *Download, addr netip.AddrPort, pieces *bm.Bitmap, peerID proto.PeerID, rng *rand.Rand) (*remotePeer, Peer) {
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
