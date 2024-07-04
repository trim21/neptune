// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package core

func (d *Download) pieceLength(index uint32) int64 {
	if index == d.info.NumPieces-1 {
		return d.info.LastPieceSize
	}

	return d.info.PieceLength
}
