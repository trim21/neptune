// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package null

import (
	"strings"
)

func NilUint8(i uint8) *uint8 {
	if i == 0 {
		return nil
	}

	return &i
}

func NilUint16(i uint16) *uint16 {
	if i == 0 {
		return nil
	}

	return &i
}

func NilString(s string) *string {
	if s == "" {
		return nil
	}

	s = strings.Clone(s)

	return &s
}
