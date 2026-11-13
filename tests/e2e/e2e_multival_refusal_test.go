package e2e

import (
	"strconv"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// multivalRefusalSelfDelegation is the power val0 self-delegates at the start
// of the refusal scenario, on top of the thousand every validator holds from
// genesis. With val1 at 1100 after the delegation of scenario 2 and val2 at
// 990 after the slash of scenario 4, val0 then holds 3100 of 6190, half of
// the bonded power, while val2 and val3 together hold 1990, under a third:
// one validator of four refusing pauses the consumer and two do not, the
// opposite of what counting heads would give. val0's node is never stopped
// afterwards, and the outages the later scenarios take (val3's and val1's
// restarts, val3's removal) leave over four fifths of the power online.
const multivalRefusalSelfDelegation = int64(2100)

// setConsumerRefusalFrom records or withdraws validator i's refusal of the
// consumer, signed by its operator key in its own provider node, and waits
// for the transaction to commit.
func (s *MultiValidatorIntegrationTestSuite) setConsumerRefusalFrom(i int, consumerID string, refused bool) { //nolint:unparam // the suite drives one consumer; the helper stays generic
	container := s.providerValRes[i].Container.ID
	stdout, stderr, err := s.dockerExec(container, []string{
		providerBinary, "tx", "vaasprovider", "set-consumer-refusal", consumerID, strconv.FormatBool(refused),
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--fees", "10000" + bondDenom,
		"-y", "-o", "json",
	})
	s.Require().NoErrorf(err, "val%d failed to set refusal=%v: stderr=%s", i, refused, stderr.String())
	s.requireTxCommittedOn(container, providerBinary, providerHomePath, stdout.Bytes())
}

// selfDelegateFrom delegates power to validator i from its own operator
// account, signed by its operator key in its own provider node, and waits
// for the transaction to commit.
func (s *MultiValidatorIntegrationTestSuite) selfDelegateFrom(i int, valoper string, power int64) {
	amount := sdk.TokensFromConsensusPower(power, sdk.DefaultPowerReduction).String() + bondDenom
	container := s.providerValRes[i].Container.ID
	stdout, stderr, err := s.dockerExec(container, []string{
		providerBinary, "tx", "staking", "delegate", valoper, amount,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y", "-o", "json",
	})
	s.Require().NoErrorf(err, "val%d failed to self-delegate %s: stderr=%s", i, amount, stderr.String())
	s.requireTxCommittedOn(container, providerBinary, providerHomePath, stdout.Bytes())
}

// providerPowerShare returns the share of the provider's consensus power the
// validators at consAddrs hold together.
func (s *MultiValidatorIntegrationTestSuite) providerPowerShare(consAddrs ...string) float64 {
	vals, err := s.queryProviderNetValidators()
	s.Require().NoError(err, "failed to query the provider's validators")
	powers := s.valAddrPowerMap(vals)
	var held, total int64
	for _, power := range powers {
		total += power
	}
	for _, addr := range consAddrs {
		power, ok := powers[addr]
		s.Require().Truef(ok, "%s is not in the provider's consensus set", addr)
		held += power
	}
	return float64(held) / float64(total)
}

// testRefusalThresholdIsStakeWeighted: the refusal pause threshold counts
// bonded stake, not refusing validators. val0 self-delegates up to half of
// the bonded power (multivalRefusalSelfDelegation). val2 and val3 refuse the
// consumer, two validators of four with under a third of the power between
// them, and the consumer stays launched; they withdraw and val0 refuses
// alone, one validator of four with half of the power, and the consumer
// pauses. val0 withdraws, governance resumes the consumer, and it syncs
// again.
func (s *MultiValidatorIntegrationTestSuite) testRefusalThresholdIsStakeWeighted() {
	s.Run("the refusal pause threshold counts stake: two of four refusing with under a third leave the consumer launched, one with half pauses it", func() {
		const consumerID = "0"
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"the consumer must be launched before the refusal scenario")
		val0, val2, val3 := s.validator(0), s.validator(2), s.validator(3)

		s.T().Log("val0 self-delegates up to half of the bonded power...")
		powerBefore, ok := s.providerPowerOf(val0.consAddr)
		s.Require().Truef(ok, "val0 (%s) is not in the provider's consensus set", val0.consAddr)
		s.selfDelegateFrom(0, val0.valoper, multivalRefusalSelfDelegation)
		s.Require().Eventuallyf(func() bool {
			power, ok := s.providerPowerOf(val0.consAddr)
			return ok && power == powerBefore+multivalRefusalSelfDelegation
		}, time.Minute, 2*time.Second,
			"the provider's consensus set never showed val0 at %d power", powerBefore+multivalRefusalSelfDelegation)

		// The shares the two refusals are judged by, on the powers the
		// earlier scenarios left: the pair's under the one-third threshold,
		// val0's alone at or above it.
		pair, alone := s.providerPowerShare(val2.consAddr, val3.consAddr), s.providerPowerShare(val0.consAddr)
		s.T().Logf("val2 and val3 hold %.3f of the bonded power between them, val0 %.3f alone", pair, alone)
		s.Require().Lessf(pair, 1.0/3, "val2 and val3 must hold under a third of the bonded power, not %.3f", pair)
		s.Require().GreaterOrEqualf(alone, 1.0/3, "val0 must hold at least a third of the bonded power, not %.3f", alone)

		s.T().Log("val2 and val3 refuse the consumer: two validators of four, under a third of the bonded power...")
		s.setConsumerRefusalFrom(2, consumerID, true)
		s.setConsumerRefusalFrom(3, consumerID, true)
		s.Require().Never(func() bool {
			phase := s.queryProviderConsumerPhase(consumerID)
			return phase != "" && phase != phaseLaunched
		}, 20*time.Second, 4*time.Second,
			"two validators refusing with under a third of the bonded power must not pause the consumer")

		s.T().Log("val2 and val3 withdraw, and val0 refuses alone: one validator of four, half of the bonded power...")
		s.setConsumerRefusalFrom(2, consumerID, false)
		s.setConsumerRefusalFrom(3, consumerID, false)
		s.setConsumerRefusalFrom(0, consumerID, true)
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phasePaused
		}, time.Minute, 2*time.Second,
			"one validator refusing with half of the bonded power must pause the consumer")

		s.T().Log("val0 withdraws its refusal and governance resumes the consumer...")
		s.setConsumerRefusalFrom(0, consumerID, false)
		s.submitAndPassProposal(s.resumeProposalJSON(consumerID, "Resume consumer (refusal withdrawn)"))
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseLaunched
		}, time.Minute, 2*time.Second,
			"the consumer did not resume after governance approval")

		s.T().Log("the resumed consumer syncs its validator set again...")
		s.waitForConsumerSync(consumerID)
	})
}
