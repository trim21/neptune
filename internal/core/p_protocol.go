// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/docker/go-units"
	"github.com/fatih/color"
	"github.com/trim21/errgo"
	"github.com/trim21/go-bencode"

	"neptune/internal/pkg/assert"
	"neptune/internal/pkg/bm"
	"neptune/internal/proto"
)

type Event struct {
	Bitmap       *bm.Bitmap
	Res          *proto.ChunkResponse
	ExtPex       proto.ExtPex
	ExtHandshake proto.ExtHandshake
	Req          proto.ChunkRequest
	Index        uint32
	Port         uint16
	ExtensionID  proto.ExtensionMessage
	Event        proto.Message
	keepAlive    bool
	Ignored      bool
}

func (p *Peer) DecodeEvents() (Event, error) {
	err := p.Conn.SetReadDeadline(time.Now().Add(time.Minute * 3))
	if err != nil {
		return Event{}, err
	}

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
		var b byte
		b, err = p.r.ReadByte()
		if err != nil {
			return event, err
		}

		extMsgID := proto.ExtensionMessage(b)

		if extMsgID == proto.ExtensionHandshake {
			var raw = make([]byte, size-2)
			_, err = io.ReadFull(p.r, raw)
			if err != nil {
				return event, err
			}

			err = bencode.Unmarshal(raw, &event.ExtHandshake)
			return event, err
		}

		if extMsgID == p.extDontHaveID.Load() {
			assert.Equal(size, 6)

			_, err = io.ReadFull(p.r, p.readBuf[:])
			if err != nil {
				return event, err
			}

			event.Index = binary.BigEndian.Uint32(p.readBuf[:])
			return event, err
		}

		if extMsgID == p.extPexID.Load() {
			var raw = make([]byte, size-2)

			_, err = io.ReadFull(p.r, raw)
			if err != nil {
				return event, err
			}

			err = bencode.Unmarshal(raw, &event.ExtPex)
			return event, err
		}

		// unknown events
		event.Ignored = true
		_, err = io.CopyN(io.Discard, p.r, int64(size-2))
		return event, err
	case proto.BitCometExtension:
	}

	// unknown events
	_, err = io.CopyN(io.Discard, p.r, int64(size-1))
	return event, err
}

func (p *Peer) decodeBitfield(eventSize uint32) (Event, error) {
	eventSize = eventSize - 1

	if eventSize != p.d.bitfieldSize {
		return Event{}, errgo.Wrap(ErrPeerSendInvalidData,
			fmt.Sprintf("expecting bitfield length %d, receive %d", p.d.bitfieldSize, eventSize))
	}

	buf := make([]byte, eventSize)

	n, err := io.ReadFull(p.r, buf)
	if err != nil {
		return Event{}, err
	}
	assert.Equal(n, int(eventSize))

	return Event{Event: proto.Bitfield, Bitmap: bm.FromBitfields(buf, p.d.info.NumPieces)}, nil
}

func (p *Peer) decodeCancel() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Cancel, Req: payload}, nil
}

func (p *Peer) decodeRequest() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Request, Req: payload}, nil
}

func (p *Peer) decodeReject() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Reject, Req: payload}, nil
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

	p.lastSend.Store(time.Now())

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
		if e.ExtensionID == proto.ExtensionHandshake {
			raw, err := bencode.Marshal(e.ExtHandshake)
			if err != nil {
				return err
			}

			binary.BigEndian.PutUint32(p.writeBuf[:], uint32(len(raw))+2)

			_, err = p.w.Write(p.writeBuf[:])
			if err != nil {
				return err
			}

			err = p.w.WriteByte(byte(proto.Extended))
			if err != nil {
				return err
			}

			err = p.w.WriteByte(byte(e.ExtensionID))
			if err != nil {
				return err
			}

			_, err = p.w.Write(raw)
			return err
		}

		fallthrough
	case proto.BitCometExtension:
		panic("unexpected event")
	}

	return nil
}
