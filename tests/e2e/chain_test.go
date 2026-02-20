//go:build e2e

package e2e

const (
	providerBinary   = "provider"
	consumerBinary   = "consumer"
	providerHomePath = "/home/nonroot/.provider"
	consumerHomePath = "/home/nonroot/.consumer"
	bondDenom        = "uatone"
	providerChainID  = "provider-e2e"
	consumerChainID  = "consumer-e2e"
)

// chain represents a Cosmos chain instance (either provider or consumer).
// Configuration is managed via Docker exec commands against the chain binary.
type chain struct {
	dataDir string
	id      string
}
