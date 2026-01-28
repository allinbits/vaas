//go:build tools

package devdeps

import (
	// linter
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"

	// mocks
	_ "go.uber.org/mock/mockgen"

	_ "golang.org/x/vuln/cmd/govulncheck"
)
