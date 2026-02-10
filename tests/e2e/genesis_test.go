package e2e

import (
	"encoding/json"
	"fmt"
	"os"
)

// patchConsumerGenesisWithProviderData patches the consumer genesis file by
// merging provider's consumer-genesis data into app_state.vaasconsumer.
// This is equivalent to: jq --slurpfile cg consumer_genesis.json '.app_state.vaasconsumer = $cg[0]'
func patchConsumerGenesisWithProviderData(genesisFilePath string, consumerGenesisJSON []byte) error {
	bz, err := os.ReadFile(genesisFilePath)
	if err != nil {
		return fmt.Errorf("failed to read genesis file: %w", err)
	}

	var genesis map[string]interface{}
	if err := json.Unmarshal(bz, &genesis); err != nil {
		return fmt.Errorf("failed to unmarshal genesis: %w", err)
	}

	appState, ok := genesis["app_state"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("app_state not found or not a map")
	}

	var consumerGenesis interface{}
	if err := json.Unmarshal(consumerGenesisJSON, &consumerGenesis); err != nil {
		return fmt.Errorf("failed to unmarshal consumer genesis: %w", err)
	}

	appState["vaasconsumer"] = consumerGenesis
	genesis["app_state"] = appState

	out, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(genesisFilePath, out, 0o600)
}
