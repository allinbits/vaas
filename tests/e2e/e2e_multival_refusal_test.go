package e2e

import (
	"strconv"
	"time"
)

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

// testRefusalThresholdIsStakeWeighted: the refusal pause threshold counts
// bonded stake. One of four validators refusing holds about a quarter of
// it, under the one-third threshold, and the consumer stays launched; a
// second refusing brings the coalition to about half, and the consumer
// pauses. Both withdraw, governance resumes the consumer, and it syncs
// again. The delegation of scenario 2 and the slash of scenario 4 leave the
// two shares on the same sides of the threshold.
func (s *MultiValidatorIntegrationTestSuite) testRefusalThresholdIsStakeWeighted() {
	s.Run("the refusal pause threshold counts stake: one of four refusing leaves the consumer launched, two pause it", func() {
		const consumerID = "0"
		s.Require().Equal(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"the consumer must be launched before the refusal scenario")

		s.T().Log("val0 refuses the consumer: about a quarter of the bonded power...")
		s.setConsumerRefusalFrom(0, consumerID, true)
		s.Require().Never(func() bool {
			return s.queryProviderConsumerPhase(consumerID) != phaseLaunched
		}, 20*time.Second, 4*time.Second,
			"a quarter of the bonded power refusing must not pause the consumer")

		s.T().Log("val1 refuses too: about half of the bonded power...")
		s.setConsumerRefusalFrom(1, consumerID, true)
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phasePaused
		}, time.Minute, 2*time.Second,
			"half of the bonded power refusing must pause the consumer")

		s.T().Log("both withdraw their refusals and governance resumes the consumer...")
		s.setConsumerRefusalFrom(0, consumerID, false)
		s.setConsumerRefusalFrom(1, consumerID, false)
		s.submitAndPassProposal(s.resumeProposalJSON(consumerID, "Resume consumer (refusals withdrawn)"))
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseLaunched
		}, time.Minute, 2*time.Second,
			"the consumer did not resume after governance approval")

		s.T().Log("the resumed consumer syncs its validator set again...")
		s.waitForConsumerSync(consumerID)
	})
}
