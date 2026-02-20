//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// providerRESTEndpoint returns the provider chain's REST HTTP endpoint using the Docker-assigned host port.
func (s *IntegrationTestSuite) providerRESTEndpoint() string {
	return fmt.Sprintf("http://%s", s.providerValRes[0].GetHostPort("1317/tcp"))
}

// consumerRESTEndpoint returns the consumer chain's REST HTTP endpoint using the Docker-assigned host port.
func (s *IntegrationTestSuite) consumerRESTEndpoint() string {
	return fmt.Sprintf("http://%s", s.consumerValRes[0].GetHostPort("1317/tcp"))
}

// providerRPCEndpoint returns the provider chain's RPC HTTP endpoint using the Docker-assigned host port.
func (s *IntegrationTestSuite) providerRPCEndpoint() string {
	return fmt.Sprintf("http://%s", s.providerValRes[0].GetHostPort("26657/tcp"))
}

// consumerRPCEndpoint returns the consumer chain's RPC HTTP endpoint using the Docker-assigned host port.
func (s *IntegrationTestSuite) consumerRPCEndpoint() string {
	return fmt.Sprintf("http://%s", s.consumerValRes[0].GetHostPort("26657/tcp"))
}

// queryProviderBlockHeight returns the current block height of the provider chain.
func (s *IntegrationTestSuite) queryProviderBlockHeight() (int64, error) {
	return queryBlockHeight(s.providerRPCEndpoint())
}

// queryConsumerBlockHeight returns the current block height of the consumer chain.
func (s *IntegrationTestSuite) queryConsumerBlockHeight() (int64, error) {
	return queryBlockHeight(s.consumerRPCEndpoint())
}

// queryNetValidators queries the current consensus validator set via the REST API.
func (s *IntegrationTestSuite) queryNetValidators(restEndpoint string) ([]*cmtservice.Validator, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/base/tendermint/v1beta1/validatorsets/latest", restEndpoint))
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res cmtservice.GetLatestValidatorSetResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return nil, err
	}
	return res.Validators, nil
}

// queryProviderNetValidators queries the provider chain for the latest consensus validator set.
func (s *IntegrationTestSuite) queryProviderNetValidators() ([]*cmtservice.Validator, error) {
	return s.queryNetValidators(s.providerRESTEndpoint())
}

// queryConsumerNetValidators queries the consumer chain for the latest consensus validator set.
func (s *IntegrationTestSuite) queryConsumerNetValidators() ([]*cmtservice.Validator, error) {
	return s.queryNetValidators(s.consumerRESTEndpoint())
}

// queryValidators queries the staking validator set via the REST API.
func (s *IntegrationTestSuite) queryValidators(restEndpoint string) ([]stakingtypes.Validator, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/staking/v1beta1/validators", restEndpoint))
	if err != nil {
		return nil, fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res stakingtypes.QueryValidatorsResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return nil, err
	}
	return res.Validators, nil
}

// queryProviderValidators queries the provider for its staking validator set via the REST API.
func (s *IntegrationTestSuite) queryProviderValidators() ([]stakingtypes.Validator, error) {
	return s.queryValidators(s.providerRESTEndpoint())
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
func (s *IntegrationTestSuite) queryBalance(restEndpoint, address, denom string) (string, error) {
	body, err := httpGet(fmt.Sprintf("%s/cosmos/bank/v1beta1/balances/%s", restEndpoint, address))
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request: %w", err)
	}

	var res banktypes.QueryAllBalancesResponse
	if err := s.cdc.UnmarshalJSON(body, &res); err != nil {
		return "", err
	}

	for _, coin := range res.Balances {
		if coin.Denom == denom {
			return coin.Amount.String(), nil
		}
	}
	return "0", nil
}

// queryProviderBalance queries an account balance on the provider chain.
func (s *IntegrationTestSuite) queryProviderBalance(address, denom string) (string, error) {
	return s.queryBalance(s.providerRESTEndpoint(), address, denom)
}

// queryConsumerBalance queries an account balance on the consumer chain.
func (s *IntegrationTestSuite) queryConsumerBalance(address, denom string) (string, error) {
	return s.queryBalance(s.consumerRESTEndpoint(), address, denom)
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
