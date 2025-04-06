// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package util

import (
	"fmt"
	"strings"
)

// -AZ2060- style peer id.
var azStyleMapping = map[[2]byte]string{
	{'N', 'E'}: "Neptune", // ourselves

	{'A', 'G'}: "Ares",
	{'A', '~'}: "Ares",
	{'A', 'R'}: "Arctic",
	{'A', 'V'}: "Avicora",
	{'A', 'X'}: "BitPump",
	{'A', 'Z'}: "Azureus",
	{'B', 'B'}: "BitBuddy",
	{'B', 'C'}: "BitComet",
	{'B', 'F'}: "Bitflu",
	{'B', 'G'}: "BTG (uses Rasterbar libtorrent)",
	{'B', 'R'}: "BitRocket",
	{'B', 'S'}: "BTSlave",
	{'B', 'X'}: "~Bittorrent X",
	{'C', 'D'}: "Enhanced CTorrent",
	{'C', 'T'}: "CTorrent",
	{'D', 'E'}: "DelugeTorrent",
	{'D', 'P'}: "Propagate Data Client",
	{'E', 'B'}: "EBit",
	{'E', 'S'}: "electric sheep",
	{'F', 'T'}: "FoxTorrent",
	{'F', 'W'}: "FrostWire",
	{'F', 'X'}: "Freebox BitTorrent",
	{'G', 'S'}: "GSTorrent",
	{'H', 'L'}: "Halite",
	{'H', 'N'}: "Hydranode",
	{'K', 'G'}: "KGet",
	{'K', 'T'}: "KTorrent",
	{'L', 'H'}: "LH-ABC",
	{'L', 'P'}: "Lphant",
	{'L', 'T'}: "libtorrent",
	{'l', 't'}: "libTorrent",
	{'L', 'W'}: "LimeWire",
	{'M', 'O'}: "MonoTorrent",
	{'M', 'P'}: "MooPolice",
	{'M', 'R'}: "Miro",
	{'M', 'T'}: "MoonlightTorrent",
	{'N', 'X'}: "Net Transport",
	{'P', 'D'}: "Pando",
	{'q', 'B'}: "qBittorrent",
	{'Q', 'D'}: "QQDownload",
	{'Q', 'T'}: "Qt 4 Torrent example",
	{'R', 'T'}: "Retriever",
	{'S', '~'}: "Shareaza alpha/beta",
	{'S', 'B'}: "~Swiftbit",
	{'S', 'S'}: "SwarmScope",
	{'S', 'T'}: "SymTorrent",
	{'s', 't'}: "sharktorrent",
	{'S', 'Z'}: "Shareaza",
	{'T', 'N'}: "TorrentDotNET",
	{'T', 'R'}: "Transmission",
	{'T', 'S'}: "Torrentstorm",
	{'T', 'T'}: "TuoTu",
	{'U', 'L'}: "uLeecher!",
	{'U', 'T'}: "µTorrent",
	{'U', 'W'}: "µTorrent Web",
	{'V', 'G'}: "Vagaa",
	{'W', 'D'}: "WebTorrent Desktop",
	{'W', 'T'}: "BitLet",
	{'W', 'W'}: "WebTorrent",
	{'W', 'Y'}: "FireTorrent",
	{'X', 'L'}: "Xunlei",
	{'X', 'T'}: "XanTorrent",
	{'X', 'X'}: "Xtorrent",
	{'Z', 'T'}: "ZipTorrent",
}

func ParsePeerID(id [20]byte) string {
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
