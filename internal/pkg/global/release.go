// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package global

import (
	"fmt"

	"tyr/internal/version"
)

var UserAgent = fmt.Sprintf("Tyr/%d.%d.%d (https://github.com/trim21/tyr)", version.MAJOR, version.MINOR, version.PATCH)

const Dev = false