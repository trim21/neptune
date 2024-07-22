// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package global

const UserAgent = "Neptune/development (https://github.com/trim21/neptune)"

var Dev bool

func init() {
	Dev = true
}
