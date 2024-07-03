//go:build tools

package tools

import (
	_ "github.com/dkorunic/betteralign/cmd/betteralign"
	_ "github.com/mfridman/tparse"
	_ "golang.org/x/tools/cmd/stringer"
	_ "golang.org/x/vuln/cmd/govulncheck"
	_ "gotest.tools/gotestsum"
)
