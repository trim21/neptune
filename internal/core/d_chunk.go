package core

func (d *Download) pieceLength(index uint32) int64 {
	if index == d.info.NumPieces-1 {
		return d.info.LastPieceSize
	}

	return d.info.PieceLength
}
