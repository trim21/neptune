// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package download

import "neptune/internal/piece_picker"

// PiecePicker is imported from the piece_picker package.
type PiecePicker = piece_picker.PiecePicker

// PieceBlock is imported from the piece_picker package.
type PieceBlock = piece_picker.PieceBlock

// BlockClaim is imported from the piece_picker package.
type BlockClaim = piece_picker.BlockClaim

// PickRequest is imported from the piece_picker package.
type PickRequest = piece_picker.PickRequest

// PiecePickStrategy is imported from the piece_picker package.
type PiecePickStrategy = piece_picker.PiecePickStrategy

// RequestGate is imported from the piece_picker package.
type RequestGate = piece_picker.RequestGate

// PickerStats is imported from the piece_picker package.
type PickerStats = piece_picker.PickerStats

// DownloadingPieceInfo is imported from the piece_picker package.
type DownloadingPieceInfo = piece_picker.DownloadingPieceInfo

// Strategy constants.
const (
	StrategyRarestFirst = piece_picker.StrategyRarestFirst
	StrategySequential  = piece_picker.StrategySequential
)

// NewPiecePicker is imported from the piece_picker package.
var NewPiecePicker = piece_picker.NewPiecePicker

// NewRequestGate is imported from the piece_picker package.
var NewRequestGate = piece_picker.NewRequestGate

// PiecePickStrategyFromString is imported from the piece_picker package.
var PiecePickStrategyFromString = piece_picker.PiecePickStrategyFromString
