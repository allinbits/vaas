package e2e

// e2e_fee_distribution_test.go asserts the paying end of the consumer fee
// model: e2e_fee_pool_test.go covers funding, locks, and the gov
// subsidy/clawback paths -- i.e. money going *into* a consumer's pool and the
// depositor claims it mints -- but nothing there proves a validator is ever
// actually paid. DistributeConsumerFees pays each eligible bonded validator
// share = fees_per_epoch / num_bonded straight to its account (there is no
// intermediate reward pool to claim from), so the assertion here is a bank
// balance delta on the provider validator's own account, checked against the
// share recomputed from the chain's live parameters.

import (
	"encoding/json"
	"time"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// testFeeDistributionAccrual verifies that a funded epoch distribution
// actually credits the bonded validator's account with exactly the
// per-validator share (or a whole multiple of it, when more than one epoch
// elapses while the balance is being polled).
func (s *IntegrationTestSuite) testFeeDistributionAccrual() {
	s.Run("epoch fee distribution credits the validator account", func() {
		const consumerID = "0"

		// Fund far above one epoch fee so the distribution cannot be skipped
		// for debt regardless of what earlier sub-tests drew down. Funding is
		// done before the balance snapshot below: it debits val in feeDenom,
		// and requireTxCommitted has already confirmed it on-chain, so it can
		// never land inside the measured delta.
		s.providerFundConsumerFeePool(consumerID, "20000000"+feeDenom)

		numBonded := s.countBondedProviderValidators()
		s.Require().Positivef(numBonded, "no bonded validators on the provider")

		// share is recomputed exactly as DistributeConsumerFees does:
		// effective per-consumer fees_per_block * blocks_per_epoch, split
		// evenly (integer division) across the bonded set.
		feesPerBlock := s.queryConsumerFeesPerBlock(consumerID)
		blocksPerEpoch := s.queryBlocksPerEpoch()
		share := (feesPerBlock * blocksPerEpoch) / int64(numBonded)
		s.Require().Positivef(share, "per-validator epoch share must be positive (fees_per_block=%d, blocks_per_epoch=%d, bonded=%d)",
			feesPerBlock, blocksPerEpoch, numBonded)
		s.T().Logf("expecting per-epoch share of %d%s per validator (fees_per_block=%d, blocks_per_epoch=%d, bonded=%d)",
			share, feeDenom, feesPerBlock, blocksPerEpoch, numBonded)

		// val's account address doubles as its operator address bytes, which
		// is where DistributeConsumerFees sends the share.
		valAccount := s.providerKeyAddress("val")
		poolAddr := s.queryConsumerFeePoolAddress(consumerID)

		before := s.providerQueryBalance(valAccount, feeDenom)
		poolBefore := s.providerQueryBalance(poolAddr, feeDenom)
		s.Require().Positivef(poolBefore, "consumer %s fee pool holds no %s after funding", consumerID, feeDenom)

		var after int64
		s.Require().Eventuallyf(func() bool {
			after = s.providerQueryBalance(valAccount, feeDenom)
			return after > before
		}, 2*time.Minute, 3*time.Second,
			"validator account %s never accrued %s from the consumer %s epoch distribution (balance stuck at %d)",
			valAccount, feeDenom, consumerID, before)

		delta := after - before
		s.Require().Zerof(delta%share,
			"validator accrual %d is not a whole number of per-epoch shares (%d)", delta, share)

		// The paid shares come out of the consumer's own pool, never from
		// anywhere else on the provider.
		poolAfter := s.providerQueryBalance(poolAddr, feeDenom)
		s.Require().Lessf(poolAfter, poolBefore,
			"consumer %s fee pool did not fall while validators were paid (before=%d, after=%d)",
			consumerID, poolBefore, poolAfter)
	})
}

// countBondedProviderValidators returns the number of bonded validators on the
// provider, i.e. the denominator DistributeConsumerFees splits an epoch fee
// across.
func (s *IntegrationTestSuite) countBondedProviderValidators() int {
	vals, err := s.queryProviderValidators()
	s.Require().NoError(err, "failed to query provider validators")

	n := 0
	for _, v := range vals {
		if v.Status == stakingtypes.Bonded {
			n++
		}
	}
	return n
}

// queryConsumerFeesPerBlock returns the effective per-block fee amount charged
// to a consumer (the per-consumer override if one is set, else the module
// default).
func (s *IntegrationTestSuite) queryConsumerFeesPerBlock(consumerID string) int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-fees-per-block", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query consumer-fees-per-block %s", consumerID)

	var res struct {
		FeesPerBlock struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"fees_per_block"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode consumer-fees-per-block response: %s", stdout.String())
	s.Require().Equalf(feeDenom, res.FeesPerBlock.Denom,
		"unexpected fee denom in consumer-fees-per-block response: %s", stdout.String())
	return parseInt64(s.T(), res.FeesPerBlock.Amount)
}

// queryBlocksPerEpoch returns the provider's blocks_per_epoch parameter. The
// params query prints the Params message itself, but a "params"-wrapped
// envelope is accepted too.
func (s *IntegrationTestSuite) queryBlocksPerEpoch() int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "params",
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query provider params")

	var res struct {
		BlocksPerEpoch string `json:"blocks_per_epoch"`
		Params         struct {
			BlocksPerEpoch string `json:"blocks_per_epoch"`
		} `json:"params"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode provider params response: %s", stdout.String())

	blocksPerEpoch := res.BlocksPerEpoch
	if blocksPerEpoch == "" {
		blocksPerEpoch = res.Params.BlocksPerEpoch
	}
	s.Require().NotEmptyf(blocksPerEpoch, "provider params carry no blocks_per_epoch: %s", stdout.String())
	return parseInt64(s.T(), blocksPerEpoch)
}
