// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package util

import (
	"fmt"
	"strings"
)

// -AZ2060- style peer id
var azureusStyleMapping = map[[2]byte]string{
	[2]byte{'q', 'B'}: "qBittorrent",
	[2]byte{'T', 'R'}: "Transmission",
	[2]byte{'L', 'T'}: "libtorrent",
	[2]byte{'L', 't'}: "libTorrent",
	[2]byte{'T', 'Y'}: "Tyr",
	[2]byte{'K', 'T'}: "KTorrent",
	[2]byte{'U', 'T'}: "µTorrent",
}

func ParsePeerId(id [20]byte) string {
	if id[0] == '-' && id[7] == '-' {
		name, ok := azureusStyleMapping[[2]byte(id[1:3])]
		if !ok {
			name = strings.ToUpper(string(id[1:3]))
		}

		if id[6] == '0' {
			return fmt.Sprintf("%s/%d.%d.%d", name, id[3]-'0', id[4]-'0', id[5]-'0')
		}

		return fmt.Sprintf("%s/%d.%d.%d.%d", name, id[3]-'0', id[4]-'0', id[5]-'0', id[6]-'0')
	}

	return string(id[:6])
}
