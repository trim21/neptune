package core

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/anacrolix/torrent/bencode"
	"github.com/docker/go-units"
	"github.com/negrel/assert"
	"github.com/trim21/errgo"

	"tyr/internal/pkg/bm"
	"tyr/internal/pkg/null"
	"tyr/internal/proto"
)

func (p *Peer) keepAlive() {
	p.log.Trace().Msg("keep alive")
	timer := time.NewTicker(time.Second * 90) // 1.5 min

	defer p.cancel()
	defer timer.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-timer.C:
			t := p.lastSend.Load()
			// lastSend not set, doing handshake
			if t == nil {
				continue
			}
			if time.Now().Add(-time.Minute * 2).After(*t) {
				err := p.sendEvent(Event{keepAlive: true})
				if err != nil {
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
	n, err := io.ReadFull(p.r, p.readSizeBuf[:])
	if err != nil {
		return Event{}, err
	}

	assert.Equal(4, n)

	size := binary.BigEndian.Uint32(p.readSizeBuf[:])

	if size >= units.MiB {
		return Event{}, ErrPeerSendInvalidData
	}

	// keep alive
	if size == 0 {
		// keep alive
		return Event{keepAlive: true}, nil
	}

	//p.log.Trace().Msgf("try to decode message with length %d", size)
	n, err = io.ReadFull(p.r, p.readSizeBuf[:1])
	if err != nil {
		return Event{}, err
	}

	assert.Equal(n, 1)

	var event Event
	event.Event = proto.Message(p.readSizeBuf[0])
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
		err = binary.Read(p.r, binary.BigEndian, &event.Index)
		return event, err
	case proto.Reject:
		return p.decodeReject()
	case proto.Extended:
		if _, err = io.ReadFull(p.r, p.readSizeBuf[:1]); err != nil {
			return event, err
		}

		if p.readSizeBuf[0] == 0 {
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

	var b = make([]byte, l+8)
	n, err := io.ReadFull(p.r, b[:l])
	if err != nil {
		return Event{}, err
	}
	assert.Equal(n, int(l))

	bmLen := l/8 + 8

	var bb = make([]uint64, bmLen)
	for i := uint32(0); i < bmLen; i += 1 {
		bb[i] = binary.BigEndian.Uint64(b[i : i+8])
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
