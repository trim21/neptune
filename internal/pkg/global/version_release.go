// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package global

import "fmt"

var MAJOR = 0
var MINOR = 0
var PATCH = 0

var UserAgent = fmt.Sprintf("Tyr/%d.%d.%d (https://github.com/trim21/tyr)", MAJOR, MINOR, PATCH)

const Dev = false
