package e2e

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// consumerFeePoolAddress computes the provider-side fee pool account for a
// given consumer id. Mirrors keeper.GetConsumerFeePoolAddress:
//
//	authtypes.NewModuleAddress(fmt.Sprintf("%s-consumer-fee-pool-%s",
//	    providertypes.ModuleName, consumerId))
func consumerFeePoolAddress(consumerID string) sdk.AccAddress {
	return authtypes.NewModuleAddress(fmt.Sprintf("provider-consumer-fee-pool-%s", consumerID))
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

// providerFundAddress sends tokens from val to addr on the provider chain.
func (s *IntegrationTestSuite) providerFundAddress(addr, amount string) {
	_, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "bank", "send", "val", addr, amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", providerChainID,
		"--fees", "10000" + bondDenom,
		"-y",
	})
	s.Require().NoError(err, "failed to fund provider address %s", addr)
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
		feePoolAddr := consumerFeePoolAddress("0").String()
		s.T().Logf("consumer fee pool address: %s", feePoolAddr)

		s.T().Log("waiting for consumer to enter debt (bank send should be rejected)...")
		s.Require().Eventuallyf(func() bool {
			out, err := s.consumerBankSendDryRun()
			return err != nil || strings.Contains(out, "consumer chain is in debt")
		}, 2*time.Minute, 5*time.Second,
			"consumer did not enter debt; last dry-run did not surface debt error")

		s.T().Log("funding consumer fee pool on provider...")
		s.providerFundAddress(feePoolAddr, "10000000"+bondDenom)

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
