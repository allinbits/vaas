package e2e

const (
	providerBinary   = "provider"
	consumerBinary   = "consumer"
	providerHomePath = "/home/nonroot/.provider"
	consumerHomePath = "/home/nonroot/.consumer"
	bondDenom        = "uatone"
	feeDenom         = "uphoton"
	providerChainID  = "provider-e2e"
	consumerChainID  = "consumer-e2e"
	// initHomesPath is where an init container sees the host directory that
	// holds a chain's node homes (val0, val1, ...).
	initHomesPath = "/home/nonroot/homes"
)

// chain represents a Cosmos chain instance (either provider or consumer).
// Configuration is managed via Docker exec commands against the chain binary.
type chain struct {
	// dataDir is the host home directory of val0, the node the suite drives.
	dataDir string
	// homes is the host home directory of every validator node, by index;
	// homes[0] is dataDir.
	homes []string
	id    string
}
