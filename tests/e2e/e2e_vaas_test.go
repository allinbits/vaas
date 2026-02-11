package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// TestVAASLifecycle validates the full VAAS provider-consumer lifecycle:
// 1. Provider chain is producing blocks
// 2. Consumer chain is producing blocks
// 3. Provider has the consumer chain registered
// 4. Consumer has provider info
// 5. Validator sets match between provider and consumer
func (s *IntegrationTestSuite) testVAASLifecycle() {
	// 1. Verify provider is producing blocks
	s.T().Log("verifying provider is producing blocks...")
	providerHeight, err := s.queryProviderBlockHeight()
	s.Require().NoError(err, "failed to query provider block height")
	s.Require().Greater(providerHeight, int64(0), "provider block height should be positive")
	s.T().Logf("provider block height: %d", providerHeight)

	// 2. Verify consumer is producing blocks
	s.T().Log("verifying consumer is producing blocks...")
	consumerHeight, err := s.queryConsumerBlockHeight()
	s.Require().NoError(err, "failed to query consumer block height")
	s.Require().Greater(consumerHeight, int64(0), "consumer block height should be positive")
	s.T().Logf("consumer block height: %d", consumerHeight)

	// 3. Query provider for consumer chain list
	s.T().Log("querying provider for consumer chains...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	chainsOutput, err := s.queryProviderConsumerChains(ctx)
	s.Require().NoError(err, "failed to query consumer chains")
	s.T().Logf("consumer chains: %s", chainsOutput)
	s.Require().Contains(chainsOutput, consumerChainID, "consumer chain should be registered on provider")

	// 4. Query consumer for provider info
	s.T().Log("querying consumer for provider info...")
	providerInfo, err := s.queryConsumerProviderInfo(ctx)
	s.Require().NoError(err, "failed to query provider info from consumer")
	s.T().Logf("provider info: %s", providerInfo)

	// 5. Verify validator sets match between provider and consumer
	s.T().Log("verifying validator set synchronization...")
	s.verifyValidatorSetSync()
}

// TestProviderBlockProduction verifies the provider chain continues to produce blocks.
func (s *IntegrationTestSuite) testProviderBlockProduction() {
	s.Run("check provider block production", func() {
		height1, err := s.queryProviderBlockHeight()
		s.Require().NoError(err)

		time.Sleep(5 * time.Second)

		height2, err := s.queryProviderBlockHeight()
		s.Require().NoError(err)

		s.Require().Greater(height2, height1, "provider should continue producing blocks")
		s.T().Logf("provider produced %d blocks in 5s (from %d to %d)", height2-height1, height1, height2)
	})
}

// TestConsumerBlockProduction verifies the consumer chain continues to produce blocks.
func (s *IntegrationTestSuite) testConsumerBlockProduction() {
	s.Run("check consumer block production", func() {
		height1, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err)

		time.Sleep(5 * time.Second)

		height2, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err)

		s.Require().Greater(height2, height1, "consumer should continue producing blocks")
		s.T().Logf("consumer produced %d blocks in 5s (from %d to %d)", height2-height1, height1, height2)
	})
}

// verifyValidatorSetSync checks that the validator public keys on the consumer
// match those on the provider.
func (s *IntegrationTestSuite) verifyValidatorSetSync() {
	providerVals, err := s.queryProviderNetValidators()
	s.Require().NoError(err, "failed to query provider validators")
	s.Require().NotEmpty(providerVals, "provider should have validators")

	consumerVals, err := s.queryConsumerNetValidators()
	s.Require().NoError(err, "failed to query consumer validators")
	s.Require().NotEmpty(consumerVals, "consumer should have validators")

	// Extract pub keys from both sets
	providerPubKeys := extractPubKeys(providerVals)
	consumerPubKeys := extractPubKeys(consumerVals)

	s.T().Logf("provider validators: %d, consumer validators: %d", len(providerPubKeys), len(consumerPubKeys))

	// Consumer validators should be a subset of (or equal to) provider validators
	for _, cpk := range consumerPubKeys {
		found := false
		for _, ppk := range providerPubKeys {
			if cpk == ppk {
				found = true
				break
			}
		}
		s.Require().True(found, "consumer validator pubkey %s should exist on provider", cpk)
	}
}

// extractPubKeys extracts the public key values from a validator set query result.
func extractPubKeys(validators []map[string]interface{}) []string {
	var keys []string
	for _, v := range validators {
		if pk, ok := v["pub_key"].(map[string]interface{}); ok {
			if val, ok := pk["value"].(string); ok {
				keys = append(keys, val)
			}
		}
	}
	return keys
}

// TestConsumerChainRegistration verifies that the consumer chain is properly
// registered on the provider.
func (s *IntegrationTestSuite) testConsumerChainRegistration() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	output, err := s.queryProviderConsumerChains(ctx)
	s.Require().NoError(err, "failed to query consumer chains")

	// Parse the output to verify consumer chain details
	s.Require().Contains(output, consumerChainID)

	// Try to parse as JSON for more structured verification
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err == nil {
		s.T().Logf("consumer chains response: %+v", result)
	}
}
