// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package version

import (
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
)

// quoteKey reports whether key is required to be quoted.
func quoteKey(key string) bool {
	return len(key) == 0 || strings.ContainsAny(key, "= \t\r\n\"`")
}

// quoteValue reports whether value is required to be quoted.
func quoteValue(value string) bool {
	return strings.ContainsAny(value, " \t\r\n\"`")
}

func FormatBuildInfo(info *debug.BuildInfo) string {
	buf := new(strings.Builder)

	fmt.Fprintf(buf, "go\t%s\n", info.GoVersion)

	modSize := 0
	versionSize := 0
	for _, d := range info.Deps {
		modSize = max(modSize, len(d.Path))
		versionSize = max(versionSize, len(d.Version))
	}

	for _, d := range info.Deps {
		fmt.Fprintf(buf, "dep\t%-*s %-*s %s", modSize, d.Path, versionSize, d.Version, d.Sum)
		if d.Replace == nil {
			fmt.Fprintf(buf, "\n")
		} else {
			panic(fmt.Sprintf("show replace module not support, add support first"))
			//fmt.Fprintf(buf, " => %s %s %s\n", d.Replace.Path, d.Replace.Version, d.Replace.Sum)
		}
	}

	for _, s := range info.Settings {
		key := s.Key
		if quoteKey(key) {
			key = strconv.Quote(key)
		}
		value := s.Value
		if quoteValue(value) {
			value = strconv.Quote(value)
		}
		fmt.Fprintf(buf, "build\t%s=%s\n", key, value)
	}

	return buf.String()
}
