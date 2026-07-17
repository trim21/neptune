// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/docker/go-units"
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

func (p *peerImpl) decodeEvents(event *Event) error {
	*event = Event{}

	err := p.Conn.SetReadDeadline(time.Now().Add(time.Minute * 3))
	if err != nil {
		return err
	}

	n, err := io.ReadFull(p.r, p.readBuf[:])
	if err != nil {
		return err
	}

	assert.Equal(4, n)

	size := binary.BigEndian.Uint32(p.readBuf[:])

	if size >= units.MiB {
		return ErrPeerSendInvalidData
	}

	// keep alive
	if size == 0 {
		event.keepAlive = true
		return nil
	}

	n, err = io.ReadFull(p.r, p.readBuf[:1])
	if err != nil {
		return err
	}

	assert.Equal(n, 1)

	event.Event = proto.Message(p.readBuf[0])
	var ev Event
	switch event.Event {
	case proto.Choke, proto.Unchoke,
		proto.Interested, proto.NotInterested,
		proto.HaveAll, proto.HaveNone:
		return nil
	case proto.Bitfield:
		ev, err = p.decodeBitfield(size)
		*event = ev
		return err
	case proto.Request:
		ev, err = p.decodeRequest()
		*event = ev
		return err
	case proto.Cancel:
		ev, err = p.decodeCancel()
		*event = ev
		return err
	case proto.Piece:
		ev, err = p.decodePiece(size - 1)
		*event = ev
		return err
	case proto.Port:
		err = binary.Read(p.r, binary.BigEndian, &event.Port)
		return err
	case proto.Have, proto.Suggest, proto.AllowedFast:
		if _, err = io.ReadFull(p.r, p.readBuf[:]); err != nil {
			return err
		}

		event.Index = binary.BigEndian.Uint32(p.readBuf[:])

		return nil
	case proto.Reject:
		ev, err = p.decodeReject()
		*event = ev
		return err
	case proto.Extended:
		var b byte
		b, err = p.r.ReadByte()
		if err != nil {
			return err
		}

		event.ExtensionID = proto.ExtensionMessage(b)

		if event.ExtensionID == proto.ExtensionHandshake {
			var raw = make([]byte, size-2)
			_, err = io.ReadFull(p.r, raw)
			if err != nil {
				return err
			}

			err = bencode.Unmarshal(raw, &event.ExtHandshake)
			return err
		}

		if event.ExtensionID == ourPexExtID {
			var raw = make([]byte, size-2)

			_, err = io.ReadFull(p.r, raw)
			if err != nil {
				return err
			}

			err = bencode.Unmarshal(raw, &event.ExtPex)
			return err
		}

		// unknown events
		event.Ignored = true
		_, err = p.r.Discard(int(size - 2))
		return err
	case proto.BitCometExtension:
	}

	// unknown events
	_, err = p.r.Discard(int(size - 1))
	return err
}

func (p *peerImpl) decodeBitfield(eventSize uint32) (Event, error) {
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

func (p *peerImpl) decodeCancel() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Cancel, Req: payload}, nil
}

func (p *peerImpl) decodeRequest() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Request, Req: payload}, nil
}

func (p *peerImpl) decodeReject() (Event, error) {
	payload, err := proto.ReadRequestPayload(p.r)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Reject, Req: payload}, nil
}

func (p *peerImpl) decodePiece(size uint32) (Event, error) {
	if size < proto.SizeUint32*2 || size >= defaultBlockSize*2 {
		return Event{}, ErrPeerSendInvalidData
	}

	payload, err := proto.ReadPiecePayload(p.r, size)
	if err != nil {
		return Event{}, err
	}

	return Event{Event: proto.Piece, Res: payload}, nil
}

func (p *peerImpl) write(e Event) error {
	_ = p.Conn.SetWriteDeadline(time.Now().Add(time.Minute * 3))

	p.lastSend.Store(time.Now().Unix())

	if e.keepAlive {
		p.log.Trace().Msg("send keepalive")
		return proto.SendKeepAlive(p.w)
	}

	p.log.Trace().Stringer("event", e.Event).Msg("send")

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
		p.pieceUploadRate.Update(len(e.Res.Data))
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
