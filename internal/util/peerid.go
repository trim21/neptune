// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package util

import (
	"fmt"
	"strings"
)

// -AZ2060- style peer id
var azStyleMapping = map[[2]byte]string{
	[2]byte{'N', 'E'}: "Neptune", // ourselves

	[2]byte{'A', 'G'}: "Ares",
	[2]byte{'A', '~'}: "Ares",
	[2]byte{'A', 'R'}: "Arctic",
	[2]byte{'A', 'V'}: "Avicora",
	[2]byte{'A', 'X'}: "BitPump",
	[2]byte{'A', 'Z'}: "Azureus",
	[2]byte{'B', 'B'}: "BitBuddy",
	[2]byte{'B', 'C'}: "BitComet",
	[2]byte{'B', 'F'}: "Bitflu",
	[2]byte{'B', 'G'}: "BTG (uses Rasterbar libtorrent)",
	[2]byte{'B', 'R'}: "BitRocket",
	[2]byte{'B', 'S'}: "BTSlave",
	[2]byte{'B', 'X'}: "~Bittorrent X",
	[2]byte{'C', 'D'}: "Enhanced CTorrent",
	[2]byte{'C', 'T'}: "CTorrent",
	[2]byte{'D', 'E'}: "DelugeTorrent",
	[2]byte{'D', 'P'}: "Propagate Data Client",
	[2]byte{'E', 'B'}: "EBit",
	[2]byte{'E', 'S'}: "electric sheep",
	[2]byte{'F', 'T'}: "FoxTorrent",
	[2]byte{'F', 'W'}: "FrostWire",
	[2]byte{'F', 'X'}: "Freebox BitTorrent",
	[2]byte{'G', 'S'}: "GSTorrent",
	[2]byte{'H', 'L'}: "Halite",
	[2]byte{'H', 'N'}: "Hydranode",
	[2]byte{'K', 'G'}: "KGet",
	[2]byte{'K', 'T'}: "KTorrent",
	[2]byte{'L', 'H'}: "LH-ABC",
	[2]byte{'L', 'P'}: "Lphant",
	[2]byte{'L', 'T'}: "libtorrent",
	[2]byte{'l', 't'}: "libTorrent",
	[2]byte{'L', 'W'}: "LimeWire",
	[2]byte{'M', 'O'}: "MonoTorrent",
	[2]byte{'M', 'P'}: "MooPolice",
	[2]byte{'M', 'R'}: "Miro",
	[2]byte{'M', 'T'}: "MoonlightTorrent",
	[2]byte{'N', 'X'}: "Net Transport",
	[2]byte{'P', 'D'}: "Pando",
	[2]byte{'q', 'B'}: "qBittorrent",
	[2]byte{'Q', 'D'}: "QQDownload",
	[2]byte{'Q', 'T'}: "Qt 4 Torrent example",
	[2]byte{'R', 'T'}: "Retriever",
	[2]byte{'S', '~'}: "Shareaza alpha/beta",
	[2]byte{'S', 'B'}: "~Swiftbit",
	[2]byte{'S', 'S'}: "SwarmScope",
	[2]byte{'S', 'T'}: "SymTorrent",
	[2]byte{'s', 't'}: "sharktorrent",
	[2]byte{'S', 'Z'}: "Shareaza",
	[2]byte{'T', 'N'}: "TorrentDotNET",
	[2]byte{'T', 'R'}: "Transmission",
	[2]byte{'T', 'S'}: "Torrentstorm",
	[2]byte{'T', 'T'}: "TuoTu",
	[2]byte{'U', 'L'}: "uLeecher!",
	[2]byte{'U', 'T'}: "µTorrent",
	[2]byte{'U', 'W'}: "µTorrent Web",
	[2]byte{'V', 'G'}: "Vagaa",
	[2]byte{'W', 'D'}: "WebTorrent Desktop",
	[2]byte{'W', 'T'}: "BitLet",
	[2]byte{'W', 'W'}: "WebTorrent",
	[2]byte{'W', 'Y'}: "FireTorrent",
	[2]byte{'X', 'L'}: "Xunlei",
	[2]byte{'X', 'T'}: "XanTorrent",
	[2]byte{'X', 'X'}: "Xtorrent",
	[2]byte{'Z', 'T'}: "ZipTorrent",
}

func ParsePeerId(id [20]byte) string {
	if id[0] == '-' && id[7] == '-' {
		name, ok := azStyleMapping[[2]byte(id[1:3])]
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
