package meta

import (
	"crypto/sha1"
	"errors"
	"path/filepath"

	"github.com/samber/lo"

	"tyr/internal/metainfo"
	"tyr/internal/pkg/null"
)

type File struct {
	Path   string
	Length int64
}

type Info struct {
	Name          string
	Pieces        []metainfo.Hash
	Files         []File
	TotalLength   int64
	PieceLength   int64
	LastPieceSize int64
	Hash          metainfo.Hash
	NumPieces     uint32
	Private       bool
}

var ErrNotV1Torrent = errors.New("meta info has no v1 info")
var ErrInvalidLength = errors.New("meta info has no v1 info")

func FromTorrent(m metainfo.MetaInfo) (Info, error) {
	info, err := m.UnmarshalInfo()
	if err != nil {
		return Info{}, err
	}

	// TODO: validate here
	//if !info.HasV1() {
	//	return Info{}, ErrNotV1Torrent
	//}

	var pieces = make([]metainfo.Hash, info.NumPieces())
	for i := 0; i < info.NumPieces(); i++ {
		pieces[i] = metainfo.Hash(info.Pieces[i : i+sha1.Size])
	}

	var files []File
	if len(info.Files) != 0 {
		files = lo.Map(info.Files, func(item metainfo.FileInfo, index int) File {
			return File{
				Path:   filepath.Join(item.BestPath()...),
				Length: item.Length,
			}
		})
	} else {
		files = []File{
			{
				Path:   info.BestName(),
				Length: info.TotalLength(),
			},
		}
	}

	i := Info{
		Hash:          m.HashInfoBytes(),
		Private:       null.NewFromPtr(info.Private).Value,
		Name:          info.BestName(),
		TotalLength:   info.TotalLength(),
		Pieces:        pieces,
		NumPieces:     uint32(info.NumPieces()),
		PieceLength:   info.PieceLength,
		LastPieceSize: info.TotalLength() - info.PieceLength*int64(info.NumPieces()-1),
		Files:         files,
	}

	if int64(i.NumPieces) != (i.TotalLength+i.PieceLength-1)/i.PieceLength {
		return Info{}, ErrInvalidLength
	}

	return i, nil
}
