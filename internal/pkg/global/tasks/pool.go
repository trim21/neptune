// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package tasks

import (
	"github.com/panjf2000/ants/v2"
	"github.com/samber/lo"
)

var pool = lo.Must(ants.NewPool(20, ants.WithPreAlloc(true)))
