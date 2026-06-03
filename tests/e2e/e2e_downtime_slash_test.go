package e2e

import (
	"fmt"
	"time"

	"cosmossdk.io/math"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

func (s *IntegrationTestSuite) testDowntimeSlash() {
	s.Run("no downtime slash, consumer down", func() {
		valoperAddr, tokensBefore := s.getProviderValidatorTokens()
		s.Require().False(tokensBefore.IsZero(), "validator should have tokens before downtime test")

		jailed := s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed before downtime test")

		s.T().Log("pausing consumer container to simulate downtime...")
		err := s.dkrPool.Client.PauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to pause consumer container")

		time.Sleep(10 * time.Second)

		s.T().Log("unpausing consumer container...")
		err = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[0].Container.ID)
		s.Require().NoError(err, "failed to unpause consumer container")

		s.T().Log("waiting for provider to process downtime evidence from consumer...")
		s.Require().Eventuallyf(func() bool {
			tokensAfter, err := s.getProviderValidatorTokensByAddr(valoperAddr)
			if err != nil {
				return false
			}
			return tokensAfter.Equal(tokensBefore)
		},
			3*time.Minute,
			5*time.Second,
			"validator tokens were incorrectly slashed during whole consumer chain downtime (before: %s, valoper: %s)",
			tokensBefore.String(), valoperAddr,
		)

		s.T().Log("verifying validator was not jailed after downtime evidence...")
		jailed = s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed after downtime evidence")
	})

	s.Run("individual validator downtime slash", func() {
		s.Require().GreaterOrEqual(len(s.consumerValRes), 2,
			"need at least 2 consumer validator containers for individual downtime test")

		valoperAddr, tokensBefore := s.getProviderValidatorTokens()
		s.Require().False(tokensBefore.IsZero(), "validator should have tokens before downtime test")

		jailed := s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed before downtime test")

		// Pause only consumer val1 (the second validator node). Consumer val0
		// keeps running so the chain continues producing blocks while val1 is
		// offline, triggering downtime detection on the consumer.
		s.T().Log("pausing consumer val1 container to simulate individual validator downtime...")
		err := s.dkrPool.Client.PauseContainer(s.consumerValRes[1].Container.ID)
		s.Require().NoError(err, "failed to pause consumer val1 container")

		// Wait long enough for the consumer's slashing module to detect the
		// downtime (signed_blocks_window=5, min_signed_per_window=0.05) and
		// for the evidence packet to be relayed to the provider.
		time.Sleep(30 * time.Second)

		s.T().Log("unpausing consumer val1 container...")
		err = s.dkrPool.Client.UnpauseContainer(s.consumerValRes[1].Container.ID)
		s.Require().NoError(err, "failed to unpause consumer val1 container")

		s.T().Log("waiting for provider to process downtime evidence from consumer...")
		s.Require().Eventuallyf(func() bool {
			tokensAfter, err := s.getProviderValidatorTokensByAddr(valoperAddr)
			if err != nil {
				return false
			}
			return tokensAfter.LT(tokensBefore)
		},
			3*time.Minute,
			5*time.Second,
			"validator tokens were not slashed on provider after consumer downtime (before: %s, valoper: %s)",
			tokensBefore.String(), valoperAddr,
		)

		s.T().Log("verifying validator was jailed after downtime slash...")
		s.Require().Eventuallyf(func() bool {
			return s.isProviderValidatorJailed()
		},
			2*time.Minute,
			5*time.Second,
			"validator should be jailed after downtime slash",
		)
	})
}

func (s *IntegrationTestSuite) patchConsumerSlashingParams() {
	s.patchGenesisJSON(s.consumer.dataDir+"/config/genesis.json", func(genesis map[string]any) {
		appState, ok := genesis["app_state"].(map[string]any)
		if !ok {
			return
		}
		slashing, ok := appState["slashing"].(map[string]any)
		if !ok {
			slashing = make(map[string]any)
		}
		params, ok := slashing["params"].(map[string]any)
		if !ok {
			params = make(map[string]any)
		}
		params["signed_blocks_window"] = "5"
		params["min_signed_per_window"] = "0.050000000000000000"
		params["slash_fraction_downtime"] = "0.000000000000000000"
		params["downtime_jail_duration"] = "60s"
		slashing["params"] = params
		appState["slashing"] = slashing
	})
}

func (s *IntegrationTestSuite) isProviderValidatorJailed() bool {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return false
	}
	for _, v := range vals {
		if v.Jailed {
			return true
		}
	}
	return false
}

// getProviderValidatorTokens returns the minority bonded validator's operator
// address and token amount (the one with the least tokens). This is the
// validator expected to be slashed when taken offline individually.
func (s *IntegrationTestSuite) getProviderValidatorTokens() (string, math.Int) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return "", math.ZeroInt()
	}
	var minTokens math.Int
	var minAddr string
	for _, v := range vals {
		if v.Status != stakingtypes.Bonded {
			continue
		}
		if minAddr == "" || v.Tokens.LT(minTokens) {
			minTokens = v.Tokens
			minAddr = v.OperatorAddress
		}
	}
	return minAddr, minTokens
}

// getProviderValidatorTokensByAddr returns the token amount for a specific validator by operator address.
func (s *IntegrationTestSuite) getProviderValidatorTokensByAddr(valoperAddr string) (math.Int, error) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return math.ZeroInt(), err
	}
	for _, v := range vals {
		if v.OperatorAddress == valoperAddr {
			return v.Tokens, nil
		}
	}
	return math.ZeroInt(), fmt.Errorf("validator %s not found", valoperAddr)
}
