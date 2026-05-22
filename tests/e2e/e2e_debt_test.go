package e2e

import (
	"encoding/json"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// queryConsumerFeePoolAddress returns the provider-side fee pool account for a
// given consumer id by querying the provider chain.
func (s *IntegrationTestSuite) queryConsumerFeePoolAddress(consumerID string) string {
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "provider", "consumer-chain", consumerID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoError(err, "failed to query consumer-chain %s", consumerID)

	var res struct {
		FeePoolAddress string `json:"fee_pool_address"`
	}
	s.Require().NoError(json.Unmarshal(stdout.Bytes(), &res),
		"failed to decode consumer-chain response: %s", stdout.String())
	s.Require().NotEmpty(res.FeePoolAddress, "fee_pool_address is empty for consumer %s", consumerID)
	return res.FeePoolAddress
}

// consumerUserBech32 returns the bech32 address of the consumer's "user"
// account. It queries the key ring inside the consumer container.
func (s *IntegrationTestSuite) consumerUserBech32() string {
	stdout, _, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "keys", "show", "user", "-a",
		"--home", consumerHomePath,
		"--keyring-backend", "test",
	})
	s.Require().NoError(err, "failed to get consumer user address")
	return strings.TrimSpace(stdout.String())
}

// consumerBankSendDryRun attempts a tiny bank send from user to user on the
// consumer in simulate mode. The simulation traverses the ante chain, so the
// debt gate fires here just like it would for a real broadcast. Returns the
// stderr output so callers can inspect rejection reasons.
func (s *IntegrationTestSuite) consumerBankSendDryRun() (string, error) {
	user := s.consumerUserBech32()
	_, stderr, err := s.dockerExec(s.consumerValRes[0].Container.ID, []string{
		consumerBinary, "tx", "bank", "send", user, user, "1" + bondDenom,
		"--home", consumerHomePath,
		"--keyring-backend", "test",
		"--chain-id", consumerChainID,
		"--fees", "1000" + bondDenom,
		"--dry-run",
		"-y",
	})
	return stderr.String(), err
}

// providerFundAddress sends `amount` from val to `addr` on the provider chain
// and blocks until the recipient's balance for the funded denom has grown,
// so callers can immediately issue txs from `addr`.
func (s *IntegrationTestSuite) providerFundAddress(addr, amount string) {
	coin, err := sdk.ParseCoinNormalized(amount)
	s.Require().NoError(err, "invalid amount %q", amount)
	before := s.providerQueryBalance(addr, coin.Denom)

	_, _, err = s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "bank", "send", "val", addr, amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err, "failed to fund provider address %s", addr)

	s.Require().Eventuallyf(func() bool {
		return s.providerQueryBalance(addr, coin.Denom) > before
	}, 30*time.Second, 2*time.Second,
		"balance of %s in %s did not grow after fund (before=%d)", addr, coin.Denom, before)
}

// providerFundConsumerFeePool deposits `amount` into the named consumer's
// fee pool via MsgFundConsumerFeePool, signed by val.
func (s *IntegrationTestSuite) providerFundConsumerFeePool(consumerID, amount string) {
	_, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "provider", "fund-consumer-fee-pool",
		consumerID, amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err, "failed to fund consumer %s fee pool", consumerID)
	time.Sleep(3 * time.Second)
}

// testConsumerDebtFlow exercises the end-to-end fee-debt lifecycle:
//  1. Consumer's fee pool starts empty, so after the first epoch the provider
//     marks the consumer as in debt and propagates the flag on the next VSC
//     packet.
//  2. A non-ibc.core / non-cosmos.gov tx on the consumer (bank send) is
//     rejected by the debt ante gate.
//  3. Funding the fee pool on the provider clears the debt; the next VSC
//     packet propagates the cleared flag.
//  4. The same tx now passes the ante gate.
func (s *IntegrationTestSuite) testConsumerDebtFlow() {
	s.Run("consumer debt flow", func() {
		// First consumer registered in this suite has id "0".
		feePoolAddr := s.queryConsumerFeePoolAddress("0")
		s.T().Logf("consumer fee pool address: %s", feePoolAddr)

		s.T().Log("waiting for consumer to enter debt (bank send should be rejected)...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			return err != nil || strings.Contains(out, "consumer chain is in debt")
		}, 2*time.Minute, 5*time.Second,
			"consumer did not enter debt; last dry-run did not surface debt error")

		s.T().Log("funding consumer fee pool on provider...")
		s.providerFundConsumerFeePool("0", "10000000"+bondDenom)

		s.T().Log("waiting for consumer to exit debt (bank send should succeed)...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			if err != nil {
				return false
			}
			return !strings.Contains(out, "consumer chain is in debt")
		}, 2*time.Minute, 5*time.Second,
			"consumer did not exit debt after fee pool was funded")
	})
}
