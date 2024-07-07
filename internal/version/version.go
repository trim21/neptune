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
	"bytes"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"text/template"

	"github.com/samber/lo"
)

// Build information. Populated at build-time.
var (
	Version   string
	Revision  string
	Ref       string
	BuildDate string
	GoOS      = runtime.GOOS
	GoArch    = runtime.GOARCH

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
	if Dev {
		Version += " (development)"
	}

	m := map[string]string{
		"version":   Version,
		"revision":  getRevision(),
		"ref":       Ref,
		"buildDate": BuildDate,
		"platform":  GoOS + "/" + GoArch,
		"goVersion": runtime.Version(),
		"tags":      getTags(),
	}
	t := template.Must(template.New("version").Parse(versionInfoTmpl))

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "version", m); err != nil {
		panic(err)
	}
	s := strings.Split(buf.String(), "\n")
	return strings.Join(lo.Filter(s, func(item string, index int) bool {
		if item != "" {
			return true
		}

		return false
	}), "\n")
}

func getRevision() string {
	if Revision != "" {
		return Revision
	}
	return computedRevision
}

func getTags() string {
	return computedTags
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
