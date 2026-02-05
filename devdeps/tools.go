//go:build tools

package devdeps

import (
	// linter
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"

	_ "golang.org/x/vuln/cmd/govulncheck"
)
