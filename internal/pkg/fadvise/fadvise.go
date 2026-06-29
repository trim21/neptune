// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package fadvise

// POSIX_FADV_* advice constants as defined by POSIX.1-2001.
// These values are stable across all platforms that implement posix_fadvise.
const (
	AdvNormal     = 0x0 // POSIX_FADV_NORMAL
	AdvRandom     = 0x1 // POSIX_FADV_RANDOM
	AdvSequential = 0x2 // POSIX_FADV_SEQUENTIAL
	AdvWillNeed   = 0x3 // POSIX_FADV_WILLNEED
	AdvDontNeed   = 0x4 // POSIX_FADV_DONTNEED
	AdvNoReuse    = 0x5 // POSIX_FADV_NOREUSE
)
