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
	"runtime"
	"runtime/debug"
	"strings"
	"text/template"
)

// Build information. Populated at build-time.
var (
	Version   string
	Revision  string
	Ref       string
	BuildDate string
	GoVersion = runtime.Version()
	GoOS      = runtime.GOOS
	GoArch    = runtime.GOARCH

	computedRevision string
	computedTags     string
	dirty            bool
)

// versionInfoTmpl contains the template used by Info.
var versionInfoTmpl = `
ref:        {{.ref}}
revision:   {{.revision}}
build date: {{.buildDate}}
platform:   {{.platform}}
build tags: {{.tags}}
`

// Print returns version information.
func Print() string {
	m := map[string]string{
		"version":   Version,
		"revision":  GetRevision(),
		"ref":       Ref,
		"buildDate": BuildDate,
		"platform":  GoOS + "/" + GoArch,
		"tags":      GetTags(),
	}
	t := template.Must(template.New("version").Parse(versionInfoTmpl))

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "version", m); err != nil {
		panic(err)
	}
	return strings.TrimSpace(buf.String())
}

func GetRevision() string {
	if Revision != "" {
		return Revision
	}
	return computedRevision
}

func GetTags() string {
	return computedTags
}

func init() {
	computedRevision, computedTags = computeRevision()
}

func computeRevision() (string, string) {
	var (
		rev      = "unknown"
		tags     = "unknown"
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
		dirty = true
		return rev + "-modified", tags
	}
	return rev, tags
}
