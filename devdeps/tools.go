//go:build tools

package devdeps

import (
	// linter
	_ "github.com/golangci/golangci-lint/cmd/golangci-lint"

	// mocks
	_ "github.com/golang/mock/mockgen"

	_ "golang.org/x/vuln/cmd/govulncheck"
)
