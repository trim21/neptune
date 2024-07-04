// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build release

package web

import (
	"embed"
	"io/fs"

	"github.com/samber/lo"
)

//go:embed frontend
var _static embed.FS

var frontendFS fs.FS = lo.Must(fs.Sub(_static, "frontend"))
