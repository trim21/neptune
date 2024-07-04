package metainfo

type Info struct {
	PieceLength int64  `bencode:"piece length"` // BEP3
	Pieces      []byte `bencode:"pieces"`       // BEP3
	Name        string `bencode:"name"`         // BEP3
	NameUtf8    string `bencode:"name.utf-8,omitempty"`
	Length      int64  `bencode:"length,omitempty"`  // BEP3, mutually exclusive with Files
	Private     *bool  `bencode:"private,omitempty"` // BEP27

	Source string     `bencode:"source,omitempty"`
	Files  []FileInfo `bencode:"files,omitempty"` // BEP3, mutually exclusive with Length

	// BEP 52 (BitTorrent v2)
	MetaVersion int64 `bencode:"meta version,omitempty"`
}

func (info *Info) TotalLength() int64 {
	if len(info.Files) == 0 {
		return info.Length
	}

	var ret int64
	for _, fi := range info.Files {
		ret += fi.Length
	}

	return ret
}

func (info *Info) NumPieces() (num int) {
	return len(info.Pieces) / 20
}

func (info *Info) BestName() string {
	if info.NameUtf8 != "" {
		return info.NameUtf8
	}
	return info.Name
}
