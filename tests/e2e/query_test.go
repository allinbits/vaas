package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// queryProviderStatus queries the provider chain status via RPC.
func (s *IntegrationTestSuite) queryProviderStatus() (map[string]interface{}, error) {
	bz, err := httpGet("http://localhost:26657/status")
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// queryConsumerStatus queries the consumer chain status via RPC.
func (s *IntegrationTestSuite) queryConsumerStatus() (map[string]interface{}, error) {
	bz, err := httpGet("http://localhost:26667/status")
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// queryProviderBlockHeight returns the current block height of the provider chain.
func (s *IntegrationTestSuite) queryProviderBlockHeight() (int64, error) {
	return queryBlockHeight("http://localhost:26657")
}

// queryConsumerBlockHeight returns the current block height of the consumer chain.
func (s *IntegrationTestSuite) queryConsumerBlockHeight() (int64, error) {
	return queryBlockHeight("http://localhost:26667")
}

// queryProviderValidators queries the provider for its validator set using REST API.
func (s *IntegrationTestSuite) queryProviderValidators() ([]map[string]interface{}, error) {
	bz, err := httpGet("http://localhost:1317/cosmos/staking/v1beta1/validators")
	if err != nil {
		return nil, err
	}

	var result struct {
		Validators []map[string]interface{} `json:"validators"`
	}
	if err := json.Unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return result.Validators, nil
}

// queryConsumerProviderInfo queries the consumer chain for its provider info
// using Docker exec (since the consumer REST API may not be accessible for
// this specific query).
func (s *IntegrationTestSuite) queryConsumerProviderInfo(ctx context.Context) (string, error) {
	stdout, _, err := s.dockerExec(ctx, s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "query", "vaasconsumer", "provider-info",
		"--home", consumerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// queryProviderConsumerChains queries the provider for registered consumer chains.
func (s *IntegrationTestSuite) queryProviderConsumerChains(ctx context.Context) (string, error) {
	stdout, _, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "list-consumer-chains",
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// queryBalance queries an account balance via the REST API.
func (s *IntegrationTestSuite) queryBalance(apiEndpoint, address, denom string) (string, error) {
	bz, err := httpGet(fmt.Sprintf("%s/cosmos/bank/v1beta1/balances/%s", apiEndpoint, address))
	if err != nil {
		return "", err
	}

	var result struct {
		Balances []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(bz, &result); err != nil {
		return "", err
	}

	for _, b := range result.Balances {
		if b.Denom == denom {
			return b.Amount, nil
		}
	}

	return "0", nil
}

// queryProviderBalance queries an account balance on the provider chain.
func (s *IntegrationTestSuite) queryProviderBalance(address, denom string) (string, error) {
	return s.queryBalance("http://localhost:1317", address, denom)
}

// queryConsumerBalance queries an account balance on the consumer chain.
func (s *IntegrationTestSuite) queryConsumerBalance(address, denom string) (string, error) {
	return s.queryBalance("http://localhost:1327", address, denom)
}

// queryProviderNetValidators queries the provider chain for the latest validators
// at a specific block height via RPC.
func (s *IntegrationTestSuite) queryProviderNetValidators() ([]map[string]interface{}, error) {
	bz, err := httpGet("http://localhost:26657/validators")
	if err != nil {
		return nil, err
	}

	var result struct {
		Result struct {
			Validators []map[string]interface{} `json:"validators"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return result.Result.Validators, nil
}

// queryConsumerNetValidators queries the consumer chain for the latest validators
// at a specific block height via RPC.
func (s *IntegrationTestSuite) queryConsumerNetValidators() ([]map[string]interface{}, error) {
	bz, err := httpGet("http://localhost:26667/validators")
	if err != nil {
		return nil, err
	}

	var result struct {
		Result struct {
			Validators []map[string]interface{} `json:"validators"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bz, &result); err != nil {
		return nil, err
	}
	return result.Result.Validators, nil
}

// queryProviderConsumerGenesis queries the provider for a specific consumer's genesis.
func (s *IntegrationTestSuite) queryProviderConsumerGenesis(ctx context.Context, consumerID string) (string, error) {
	stdout, stderr, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-genesis", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", fmt.Errorf("query failed: %w (stderr: %s)", err, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	return output, nil
}
