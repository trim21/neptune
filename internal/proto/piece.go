package proto

import (
	"encoding/binary"
	"io"

	"tyr/internal/pkg/as"
)

type ChunkResponse struct {
	// len(Data) should match request
	Data       []byte
	Begin      uint32
	PieceIndex uint32
}

func (c ChunkResponse) Request() ChunkRequest {
	return ChunkRequest{
		PieceIndex: c.PieceIndex,
		Begin:      c.Begin,
		Length:     as.Uint32(len(c.Data)),
	}
}

func SendPiece(conn io.Writer, r ChunkResponse) error {
	var b [sizeUint32 + sizeByte + sizeUint32*2]byte

	binary.BigEndian.PutUint32(b[:], uint32(len(r.Data)+sizeByte+sizeUint32*2))
	b[4] = byte(Piece)
	binary.BigEndian.PutUint32(b[sizeUint32+sizeByte:], r.PieceIndex)
	binary.BigEndian.PutUint32(b[sizeUint32+sizeByte+sizeUint32:], r.Begin)

	_, err := conn.Write(b[:])
	if err != nil {
		return err
	}

	_, err = conn.Write(r.Data)
	return err
}

func ReadPiecePayload(conn io.Reader, size uint32) (ChunkResponse, error) {
	var b [sizeUint32 * 2]byte

	_, err := io.ReadFull(conn, b[:])
	if err != nil {
		return ChunkResponse{}, err
	}

	var payload = ChunkResponse{
		PieceIndex: binary.BigEndian.Uint32(b[:]),
		Begin:      binary.BigEndian.Uint32(b[sizeUint32 : sizeUint32*2]),
		Data:       make([]byte, size-sizeUint32*2),
	}

	//buf := mempool.GetSlice()

	//payload.Data = slices.Grow(buf, int(size-sizeUint32*2))
	//payload.Data = payload.Data[:size-sizeUint32*2]

	_, err = io.ReadFull(conn, payload.Data)

	return payload, err
}
