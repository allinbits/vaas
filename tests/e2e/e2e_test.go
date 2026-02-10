package e2e

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

// TestIntegrationTestSuite is the entry point for the e2e test suite.
// It bootstraps the full provider-consumer-relayer lifecycle and runs all tests.
func TestIntegrationTestSuite(t *testing.T) {
	suite.Run(t, new(IntegrationTestSuite))
}
