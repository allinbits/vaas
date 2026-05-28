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

		s.T().Log("verifying validator was not jailed after downtime slash...")
		jailed = s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed after downtime slash")
	})

	s.Run("downtime slash", func() {
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

		// TODO: create multiple validators in e2e test and only shutdown one validator on the consumer chain.
		_ = valoperAddr
		// s.T().Log("waiting for provider to process downtime evidence from consumer...")
		// s.Require().Eventuallyf(func() bool {
		// 	tokensAfter, err := s.getProviderValidatorTokensByAddr(valoperAddr)
		// 	if err != nil {
		// 		return false
		// 	}
		// 	return tokensAfter.LT(tokensBefore)
		// },
		// 	3*time.Minute,
		// 	5*time.Second,
		// 	"validator tokens were not slashed on provider after consumer downtime (before: %s, valoper: %s)",
		// 	tokensBefore.String(), valoperAddr,
		// )

		s.T().Log("verifying validator was not jailed after downtime slash...")
		jailed = s.isProviderValidatorJailed()
		s.Require().False(jailed, "validator should not be jailed after downtime slash")
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

// getProviderValidatorTokens returns the first bonded validator's operator address and token amount.
func (s *IntegrationTestSuite) getProviderValidatorTokens() (string, math.Int) {
	vals, err := s.queryProviderValidators()
	if err != nil {
		return "", math.ZeroInt()
	}
	for _, v := range vals {
		if v.Status == stakingtypes.Bonded {
			return v.OperatorAddress, v.Tokens
		}
	}
	return "", math.ZeroInt()
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
