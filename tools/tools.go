//go:build tools

package tools

import (
	_ "github.com/dkorunic/betteralign/cmd/betteralign"
	_ "golang.org/x/tools/cmd/stringer"
	_ "golang.org/x/vuln/cmd/govulncheck"
	_ "gotest.tools/gotestsum"
)
