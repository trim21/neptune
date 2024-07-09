// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/anacrolix/torrent/bencode"
	"github.com/docker/go-units"
	"github.com/fatih/color"
	"github.com/samber/lo"
	"github.com/trim21/errgo"

	"tyr/internal/pkg/assert"
	"tyr/internal/pkg/bm"
	"tyr/internal/pkg/mempool"
	"tyr/internal/pkg/null"
	"tyr/internal/proto"
)

func (p *Peer) keepAlive() {
	timer := time.NewTicker(time.Second * 90) // 1.5 min
	defer timer.Stop()

	defer p.cancel()

	for {
		select {
		case <-p.ctx.Done():
			p.log.Trace().Msg("ctx done, stop keep alive")
			return
		case <-timer.C:
			t := p.lastSend.Load()
			// lastSend not set, doing handshake
			if t == nil || t.Before(time.Now().Add(-time.Second*1)) {
				err := p.sendEvent(Event{keepAlive: true})
				if err != nil {
					p.log.Trace().Err(err).Msg("failed to send keep alive message, stop keep alive")
					return
				}
			}
		}
	}
}

type Event struct {
	Bitmap       *bm.Bitmap
	Res          proto.ChunkResponse
	ExtHandshake extension
	Req          proto.ChunkRequest
	Index        uint32
	Port         uint16
	Event        proto.Message
	keepAlive    bool
	Ignored      bool
}

type extension struct {
	V           null.String `bencode:"v"`
	QueueLength null.Uint32 `bencode:"reqq"`
}

func (p *Peer) DecodeEvents() (Event, error) {
	_ = p.Conn.SetReadDeadline(time.Now().Add(time.Minute * 3))
	n, err := io.ReadFull(p.r, p.readBuf[:])
	if err != nil {
		return Event{}, err
	}

	assert.Equal(4, n)

	size := binary.BigEndian.Uint32(p.readBuf[:])

	if size >= units.MiB {
		return Event{}, ErrPeerSendInvalidData
	}

	// keep alive
	if size == 0 {
		// keep alive
		return Event{keepAlive: true}, nil
	}

	//p.log.Trace().Msgf("try to decode message with length %d", size)
	n, err = io.ReadFull(p.r, p.readBuf[:1])
	if err != nil {
		return Event{}, err
	}

	assert.Equal(n, 1)

	var event Event
	event.Event = proto.Message(p.readBuf[0])
	//p.log.Trace().Msgf("try to decode message event %s", color.BlueString(event.Event.String()))
	switch event.Event {
	case proto.Choke, proto.Unchoke,
		proto.Interested, proto.NotInterested,
		proto.HaveAll, proto.HaveNone:
		return event, nil
	case proto.Bitfield:
		return p.decodeBitfield(size)
	case proto.Request:
		return p.decodeRequest()
	case proto.Cancel:
		return p.decodeCancel()
	case proto.Piece:
		return p.decodePiece(size - 1)
	case proto.Port:
		err = binary.Read(p.r, binary.BigEndian, &event.Port)
		return event, err
	case proto.Have, proto.Suggest, proto.AllowedFast:
		if _, err = io.ReadFull(p.r, p.readBuf[:]); err != nil {
			return event, err
		}

		event.Index = binary.BigEndian.Uint32(p.readBuf[:])

		return event, nil
	case proto.Reject:
		return p.decodeReject()
	case proto.Extended:
		if _, err = io.ReadFull(p.r, p.readBuf[:1]); err != nil {
			return event, err
		}

		if p.readBuf[0] == 0 {
			err = bencode.NewDecoder(io.LimitReader(p.r, int64(size-2))).Decode(&event.ExtHandshake)
			return event, err
		}

		event.Ignored = true
		// unknown events
		_, err = io.CopyN(io.Discard, p.r, int64(size-2))
		return event, err
	case proto.BitCometExtension:
	}

	// unknown events
	_, err = io.CopyN(io.Discard, p.r, int64(size-1))
	return event, err
}

func (p *Peer) decodeBitfield(l uint32) (Event, error) {
	l = l - 1

	if l != p.bitfieldSize {
		return Event{}, errgo.Wrap(ErrPeerSendInvalidData,
			fmt.Sprintf("expecting bitfield length %d, receive %d", p.bitfieldSize, l))
	}

	buf := mempool.GetWithCap(int(l + 64))
	defer mempool.Put(buf)

	n, err := io.ReadFull(p.r, buf.B[:l])
	if err != nil {
		return Event{}, err
	}
	assert.Equal(n, int(l))

	bmLen := l/8 + 8

	var bb = make([]uint64, bmLen)
	for i := 0; i < int(bmLen); i++ {
		bb[i] = binary.BigEndian.Uint64(buf.B[i*8 : i*8+8])
	}

	bitmap := roaring.FromDense(bb, false)

	return Event{Event: proto.Bitfield, Bitmap: bm.FromBitmap(bitmap, p.d.info.NumPieces)}, nil
}

func (p *Peer) decodeCancel() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Cancel, Req: payload}, err
}

func (p *Peer) decodeRequest() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Request, Req: payload}, err
}

func (p *Peer) decodeReject() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Reject, Req: payload}, err
}

func (p *Peer) decodePiece(size uint32) (Event, error) {
	if size >= defaultBlockSize*2 {
		return Event{}, ErrPeerSendInvalidData
	}

	payload, err := proto.ReadPiecePayload(p.r, size)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Piece, Res: payload}, nil
}

func (p *Peer) write(e Event) error {
	_ = p.Conn.SetWriteDeadline(time.Now().Add(time.Minute * 3))

	p.lastSend.Store(lo.ToPtr(time.Now()))

	if e.keepAlive {
		p.log.Trace().Msg("send keepalive")
		return proto.SendKeepAlive(p.w)
	}

	p.log.Trace().Msgf("send %s", color.BlueString(e.Event.String()))

	switch e.Event {
	case proto.Choke:
		return proto.SendChoke(p.w)
	case proto.Unchoke:
		return proto.SendUnchoke(p.w)
	case proto.Interested:
		return proto.SendInterested(p.w)
	case proto.NotInterested:
		return proto.SendNotInterested(p.w)
	case proto.Have:
		return proto.SendHave(p.w, e.Index)
	case proto.Bitfield:
		return proto.SendBitfield(p.w, e.Bitmap)
	case proto.Request:
		return proto.SendRequest(p.w, e.Req)
	case proto.Piece:
		p.ioOut.Update(len(e.Res.Data))
		return proto.SendPiece(p.w, e.Res)
	case proto.Cancel:
		return proto.SendCancel(p.w, e.Req)
	case proto.Port:
		return proto.SendPort(p.w, e.Port)
	case proto.Suggest:
		return proto.SendSuggest(p.w, e.Index)
	case proto.HaveAll, proto.HaveNone:
		return proto.SendNoPayload(p.w, e.Event)
	case proto.AllowedFast:
		return proto.SendIndexOnly(p.w, e.Event, e.Index)
	case proto.Reject:
		return proto.SendReject(p.w, e.Req)
	case proto.Extended:
		return bencode.NewEncoder(p.w).Encode(e.ExtHandshake)
	case proto.BitCometExtension:
		panic("unexpected event")
	}

	return nil
}
