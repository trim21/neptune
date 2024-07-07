// Copyright 2024 trim21 <trim21.me@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
)

// Build information. Populated at build-time.
var (
	Version   string
	Revision  string
	Ref       string
	BuildDate string

	computedRevision string
	computedTags     string
)

// versionInfoTmpl contains the template used by Info.
var versionInfoTmpl = `
version:    {{ .version }}
{{ if .ref -}} ref:        {{.ref}} {{- end }}
revision:   {{.revision}}
go version: {{.goVersion}}
{{ if .buildDate -}} build date: {{.buildDate}} {{- end }}
{{ if .tags -}} build tags: {{.tags}} {{- end }}
`

var versionOutput string

// Print returns version information.
func Print() string {
	return versionOutput
}

func gen() string {
	Version = fmt.Sprintf("%d.%d.%d", MAJOR, MINOR, PATCH)
	if dev {
		Version += " (development)"
	}

	var s = &strings.Builder{}
	fmt.Fprintf(s, "version:    %s\n", Version)
	if Ref != "" {
		fmt.Fprintf(s, "ref:        %s\n", Ref)
	}
	fmt.Fprintf(s, "revision:   %s\n", getRevision())
	fmt.Fprintf(s, "go version: %s\n", runtime.Version())

	if BuildDate != "" {
		fmt.Fprintf(s, "build date: %s\n", BuildDate)
	}

	if computedTags != "" {
		fmt.Fprintf(s, "build tags: %s\n", computedTags)
	}

	return strings.TrimSpace(s.String())
}

func getRevision() string {
	if Revision != "" {
		return Revision
	}
	return computedRevision
}

func init() {
	computedRevision, computedTags = computeRevision()
	versionOutput = gen()
}

func computeRevision() (string, string) {
	var (
		rev      = "<unknown>"
		tags     = ""
		modified bool
	)

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return rev, tags
	}
	for _, v := range buildInfo.Settings {
		if v.Key == "vcs.revision" {
			rev = v.Value
		}
		if v.Key == "vcs.modified" {
			if v.Value == "true" {
				modified = true
			}
		}
		if v.Key == "-tags" {
			tags = v.Value
		}
	}

	if modified {
		return rev + "-modified", tags
	}

	return rev, tags
}
