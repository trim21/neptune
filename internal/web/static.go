// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !release

package web

import (
	"os"
)

// FS is for development, so we don't need to restart process
var frontendFS = os.DirFS("internal/web/frontend/")
