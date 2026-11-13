package e2e

import (
	"time"
)

// multivalDelegation is what scenario 2 delegates to val1: 100 units of
// power on a thousand per validator, small enough that the worst single
// outage still leaves well over two thirds of the power online.
const multivalDelegation = "100000000" + bondDenom

// consumerPowerOf returns the voting power the consumer's current validator
// set gives consAddr, and whether consAddr is in the set.
func (s *MultiValidatorIntegrationTestSuite) consumerPowerOf(consAddr string) (int64, bool) {
	vals, err := s.queryConsumerNetValidators()
	if err != nil {
		return 0, false
	}
	power, ok := s.valAddrPowerMap(vals)[consAddr]
	return power, ok
}

// delegate delegates amount from the suite's user account to valoper on the
// provider, through val0, and waits for the transaction to commit.
func (s *MultiValidatorIntegrationTestSuite) delegate(valoper, amount string) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "tx", "staking", "delegate", valoper, amount,
		"--from", "user",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to broadcast the delegation to %s: stderr=%s", valoper, stderr.String())
	s.requireTxCommitted(stdout.Bytes())
}

// testDelegationReachesConsumer: a delegation on the provider changes one
// validator's power, and the next epoch's VSC carries that power to the
// consumer's set while the other validators keep theirs.
func (s *MultiValidatorIntegrationTestSuite) testDelegationReachesConsumer() {
	s.Run("a provider delegation reaches the consumer's validator set", func() {
		val1 := s.validator(1)
		before, ok := s.consumerPowerOf(val1.consAddr)
		s.Require().Truef(ok, "val1 (%s) is not in the consumer's set", val1.consAddr)

		others := map[int]int64{}
		for _, i := range []int{0, 2, 3} {
			power, ok := s.consumerPowerOf(s.validator(i).consAddr)
			s.Require().Truef(ok, "val%d is not in the consumer's set", i)
			others[i] = power
		}

		s.delegate(val1.valoper, multivalDelegation)

		s.Require().Eventuallyf(func() bool {
			power, ok := s.consumerPowerOf(val1.consAddr)
			return ok && power == before+100
		}, 3*time.Minute, 5*time.Second,
			"the consumer never reported val1's power at %d after the delegation", before+100)

		for i, want := range others {
			power, ok := s.consumerPowerOf(s.validator(i).consAddr)
			s.Require().Truef(ok, "val%d left the consumer's set", i)
			s.Require().Equalf(want, power, "val%d's consumer power changed with val1's delegation", i)
		}
	})
}
