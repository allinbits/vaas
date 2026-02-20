package e2e

import (
	"context"
	"strings"
	"time"
	"strconv"
)

// testProviderBlockProduction verifies the provider chain continues to produce blocks.
func (s *IntegrationTestSuite) testProviderBlockProduction() {
	s.Run("provider block production", func() {
		height1, err := s.queryProviderBlockHeight()
		s.Require().NoError(err)

		s.Require().Eventually(
			func() bool {
				height2, err := s.queryProviderBlockHeight()
				return height2 > height1 && err == nil
			},
			10*time.Second,
			time.Second,
			"provider is not producing blocks",
		)
	})
}

// testConsumerBlockProduction verifies the consumer chain continues to produce blocks.
func (s *IntegrationTestSuite) testConsumerBlockProduction() {
	s.Run("consumer block production", func() {
		height1, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err)
		s.Require().Eventually(
			func() bool {
				height2, err := s.queryConsumerBlockHeight()
				return height2 > height1 && err == nil
			},
			10*time.Second,
			time.Second,
			"consumer is not producing blocks",
		)
	})
}

// testConsumerOnProvider verifies the consumer chain is registered on the provider.
func (s *IntegrationTestSuite) testConsumerOnProvider() {
	s.Run("consumer chain registered on provider", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		chainsOutput, err := s.queryProviderConsumerChains(ctx)
		s.Require().NoError(err, "failed to query consumer chains")
		//s.T().Logf("consumer chains: %s", chainsOutput)
		s.Require().Contains(chainsOutput, consumerChainID, "consumer chain should be registered on provider")
	})
}

// testProviderConsumerChains verifies the consumer chain is registered on the provider.
func (s *IntegrationTestSuite) testProviderOnConsumer() {
	s.Run("provider chain registered on consumer", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		providerInfo, err := s.queryConsumerProviderInfo(ctx)
		s.Require().NoError(err, "failed to query provider info from consumer")
		//s.T().Logf("provider info: %s", providerInfo)
		s.Require().Contains(providerInfo, providerChainID, "provider chain should be registered on consumer")
	})
}

// testValidatorSetSync checks that the validator set on the consumer
// is synchronized to the one the provider.
func (s *IntegrationTestSuite) testValidatorSetSync() {
	s.Run("validator set sync", func() {
		providerVals, err := s.queryProviderNetValidators()
		s.Require().NoError(err, "failed to query provider validators")

		consumerVals, err := s.queryConsumerNetValidators()
		s.Require().NoError(err, "failed to query consumer validators")

		// Extract pub keys  and vp from both chains
		providerPubKeys, providerVP := extractPubKeys(providerVals)
		consumerPubKeys, consumerVP := extractPubKeys(consumerVals)

		s.Require().Equal(len(providerPubKeys), 1)
		s.Require().Equal(len(providerPubKeys), len(consumerPubKeys))
		s.Require().Equal(providerPubKeys[0], consumerPubKeys[0])
		s.Require().Equal(providerVP[0], consumerVP[0])

		// Increase delegation of val on provider chain
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Get validator operator address
		stdout, _, err := s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
			providerBinary, "keys", "show", "val", "--bech", "val", "-a",
			"--home", providerHomePath,
			"--keyring-backend", "test",
		})
		s.Require().NoError(err, "failed to get validator address")
		valAddr := strings.TrimSpace(stdout.String())

		// Delegate to trigger validator set change
		_, _, err = s.dockerExec(ctx, s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "staking", "delegate", valAddr, "1000000" + bondDenom,
			"--from", "user",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"-y",
		})
		s.Require().NoError(err, "failed to perform delegation")

		// Check increase in VP for Val0 on Provider
		var providerVPAfter []uint64
		s.Require().Eventually( func() bool {
			providerValsAfter, err := s.queryProviderNetValidators()
			s.Require().NoError(err, "failed to query provider validators")
			_ , providerVPAfter = extractPubKeys(providerValsAfter)
			return providerVPAfter[0] == providerVP[0] + 1
		},
		10*time.Second,
		time.Second)


		// Check increase in VP for Val0 on Consumer
		var consumerVPAfter []uint64
		s.Require().Eventually(
			func() bool {
				consumerValsAfter, err := s.queryConsumerNetValidators()
				s.Require().NoError(err, "failed to query consumer validators")
				_, consumerVPAfter = extractPubKeys(consumerValsAfter)
				return consumerVPAfter[0] == providerVPAfter[0]
			},
			50*time.Second,
			time.Second,
			"consumer validator set is not updated",
		)
	})
}

// extractPubKeys extracts the public key values from a validator set query result.
func extractPubKeys(validators []map[string]interface{}) ([]string,[]uint64) {
	var keys []string
	var vp []uint64
	for _, v := range validators {
		if pk, ok := v["pub_key"].(map[string]interface{}); ok {
				if val, ok := pk["value"].(string); ok {
					keys = append(keys, val)
				}
		}
		if vp_val, err := strconv.ParseUint(v["voting_power"].(string),10,64); err == nil {
				vp = append(vp, vp_val)
		}
	}
	return keys, vp
}
