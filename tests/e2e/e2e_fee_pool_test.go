package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// providerQueryBalance returns the bech32-addressed account's balance for
// `denom` on the provider, as an int64.
func (s *IntegrationTestSuite) providerQueryBalance(addr, denom string) int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "bank", "balances", addr,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err)
	var res struct {
		Balances []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"balances"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res))
	for _, b := range res.Balances {
		if b.Denom == denom {
			var n int64
			fmt.Sscanf(b.Amount, "%d", &n)
			return n
		}
	}
	return 0
}

// providerQueryFeePoolClaim returns the depositor's claim on the consumer's
// fee pool in the given denom (returns 0 if no claim).
func (s *IntegrationTestSuite) providerQueryFeePoolClaim(consumerID, depositor, denom string) int64 {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-fee-pool-claim",
		consumerID, depositor,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err)
	var res struct {
		Claim []struct {
			Denom  string `json:"denom"`
			Amount string `json:"amount"`
		} `json:"claim"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res))
	for _, c := range res.Claim {
		if c.Denom == denom {
			var n int64
			fmt.Sscanf(c.Amount, "%d", &n)
			return n
		}
	}
	return 0
}

// providerKeyAddress returns the bech32 address of the named key on the
// provider.
func (s *IntegrationTestSuite) providerKeyAddress(key string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "keys", "show", key, "-a",
		"--home", providerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err)
	return strings.TrimSpace(stdout.String())
}

// providerFundConsumerFeePoolFrom is the multi-signer variant of
// providerFundConsumerFeePool (which is hardcoded to --from val).
func (s *IntegrationTestSuite) providerFundConsumerFeePoolFrom(consumerID, from, amount string) {
	_, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "fund-consumer-fee-pool",
		consumerID, amount,
		"--from", from,
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err)
	time.Sleep(3 * time.Second)
}

// providerFundCommunityPool funds the community pool from val on the provider
// and blocks until the community-pool balance for the funded denom has grown,
// so callers can immediately issue follow-up txs without racing the previous
// tx's account-sequence commit.
func (s *IntegrationTestSuite) providerFundCommunityPool(amount string) {
	coin, err := sdk.ParseCoinNormalized(amount)
	s.Require().NoError(err, "invalid amount %q", amount)
	before := s.queryCommunityPoolBalance(coin.Denom)

	_, _, err = s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "distribution", "fund-community-pool", amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err)

	s.Require().Eventuallyf(func() bool {
		return s.queryCommunityPoolBalance(coin.Denom) > before
	}, 30*time.Second, 2*time.Second,
		"community pool %s did not grow after fund (before=%d)", coin.Denom, before)
}

// testFeePoolFundAndLockEnforcement verifies that funding works above the
// min-deposit floor during LAUNCHED, and that withdraw + sweep are rejected
// by the LAUNCHED lock. Owner-triggered sweep semantics during STOPPED/post-
// delete auto-sweep are covered by unit tests; e2e cannot easily transition
// the consumer out of LAUNCHED without a remove-consumer helper, which is
// out of scope here.
func (s *IntegrationTestSuite) testFeePoolFundAndLockEnforcement() {
	s.Run("fee pool fund and lock enforcement", func() {
		const consumerID = "0"
		denom := bondDenom
		valAddr := s.providerKeyAddress("val")
		userAddr := s.providerKeyAddress("user")

		// Make sure user has enough balance to fund above the floor.
		// Floor = fees_per_block (1000) * MinDepositBlocks (14400) = 14_400_000.
		s.providerFundAddress(userAddr, "50000000"+denom)

		// Two funders, both above the floor.
		s.providerFundConsumerFeePool(consumerID, "20000000"+denom)             // --from val
		s.providerFundConsumerFeePoolFrom(consumerID, "user", "20000000"+denom) // --from user

		valClaim := s.providerQueryFeePoolClaim(consumerID, valAddr, denom)
		userClaim := s.providerQueryFeePoolClaim(consumerID, userAddr, denom)
		s.Require().Greater(valClaim, int64(0), "val should have a non-zero claim")
		s.Require().Greater(userClaim, int64(0), "user should have a non-zero claim")

		// Non-gov withdraw during LAUNCHED is rejected by the LAUNCHED lock.
		// Use --dry-run so the full simulation pipeline runs (including the
		// handler that rejects this tx) without burning fees on a doomed tx.
		_, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "provider", "withdraw-consumer-fee-pool",
			consumerID, "1000" + denom,
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"--dry-run",
			"-y",
		})
		combined := stderr.String()
		s.Require().True(err != nil || strings.Contains(combined, "locked while consumer"),
			"non-gov withdraw during LAUNCHED should be rejected by the lock: stderr=%q, err=%v", combined, err)

		// Owner sweep during LAUNCHED is also rejected (no gov bypass on sweep).
		_, stderr, err = s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "provider", "sweep-consumer-fee-pool",
			consumerID,
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"--dry-run",
			"-y",
		})
		combined = stderr.String()
		s.Require().True(err != nil || strings.Contains(combined, "locked while consumer"),
			"owner sweep during LAUNCHED should be rejected by the lock: stderr=%q, err=%v", combined, err)
	})
}

// testFeePoolSendRestriction verifies that direct bank sends to a consumer
// fee pool address are blocked by the SendRestriction. Uses --dry-run so
// the ante chain runs (which is where the restriction fires) without
// burning fees on a doomed transaction.
func (s *IntegrationTestSuite) testFeePoolSendRestriction() {
	s.Run("fee pool send restriction", func() {
		feePoolAddr := s.queryConsumerFeePoolAddress("0")
		valAddr := s.providerKeyAddress("val")

		_, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "bank", "send", valAddr, feePoolAddr, "1" + bondDenom,
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", providerChainID,
			"--fees", "10000" + bondDenom,
			"--dry-run",
			"-y",
		})
		combined := stderr.String()
		// The restriction's error message contains "consumer fee pool".
		// Match on a distinctive substring rather than the exact error code
		// so the test tolerates future error-message tweaks.
		s.Require().True(err != nil || strings.Contains(combined, "consumer fee pool"),
			"direct bank send to fee pool should be rejected: stderr=%q, err=%v", combined, err)
	})
}

// testFeePoolGovSubsidyClawback verifies the gov-conditional path:
// gov can fund a consumer fee pool from the community pool (with the
// distribution module account credited as the depositor), and gov can
// claw back the unconsumed portion via a withdrawal proposal.
func (s *IntegrationTestSuite) testFeePoolGovSubsidyClawback() {
	s.Run("fee pool gov subsidy + clawback", func() {
		const consumerID = "0"
		denom := bondDenom
		govAddr := s.queryGovAuthority()
		distrAddr := s.queryModuleAccountAddress("distribution")

		// Seed the community pool so gov has something to spend.
		s.providerFundCommunityPool("40000000" + denom)
		cpBefore := s.queryCommunityPoolBalance(denom)
		s.Require().Greater(cpBefore, int64(0), "community pool seeded")

		fundJSON := fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgFundConsumerFeePool",
    "signer": %q,
    "consumer_id": %q,
    "amount": {"denom": %q, "amount": "20000000"}
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Subsidize consumer %s",
  "summary": "e2e gov subsidy test"
}`, govAddr, consumerID, denom, denom, consumerID)

		s.submitAndPassProposal(fundJSON)

		cpAfterFund := s.queryCommunityPoolBalance(denom)
		s.Require().Less(cpAfterFund, cpBefore, "community pool debited by subsidy")

		distrClaim := s.providerQueryFeePoolClaim(consumerID, distrAddr, denom)
		s.Require().Greater(distrClaim, int64(0), "distribution module account has a non-zero claim after gov fund")

		clawbackJSON := fmt.Sprintf(`{
  "messages": [{
    "@type": "/vaas.provider.v1.MsgWithdrawConsumerFeePool",
    "signer": %q,
    "consumer_id": %q,
    "amount": [{"denom": %q, "amount": "20000000"}]
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Clawback consumer %s subsidy",
  "summary": "e2e gov clawback test"
}`, govAddr, consumerID, denom, denom, consumerID)

		s.submitAndPassProposal(clawbackJSON)

		cpAfterClawback := s.queryCommunityPoolBalance(denom)
		s.Require().Greater(cpAfterClawback, cpAfterFund, "community pool grew back after clawback")
	})
}
