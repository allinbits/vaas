package e2e

import (
	"encoding/json"
	"time"
)

// unjailFrom unjails validator i with its operator key, signed in its own
// provider node, and waits until it reads back unjailed.
func (s *ByzantineConsumerIntegrationTestSuite) unjailFrom(i int, valoper string) {
	container := s.providerValRes[i].Container.ID
	s.Require().Eventuallyf(func() bool {
		stdout, _, err := s.dockerExec(container, []string{
			providerBinary, "tx", "slashing", "unjail",
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--fees", "10000" + bondDenom,
			"-y", "-o", "json",
		})
		if err != nil {
			return false
		}
		var res struct {
			Code int `json:"code"`
		}
		return json.Unmarshal(stdout.Bytes(), &res) == nil && res.Code == 0
	}, time.Minute, 5*time.Second, "val%d could not broadcast its unjail", i)

	s.Require().Eventuallyf(func() bool {
		jailed, _ := s.stakingValidatorState(valoper)
		return !jailed
	}, time.Minute, 2*time.Second, "val%d must read back unjailed", i)
}

// testRemovalCancelsTheFabricatedPunishment: governance removes the lying
// consumer, the one verdict that voids its evidence. The punishments the
// fabricated double-sign and the double vote over the old chain id queued
// against val2 are cancelled with it: val2 is never slashed nor tombstoned,
// and its jail opens, so it can unjail.
func (s *ByzantineConsumerIntegrationTestSuite) testRemovalCancelsTheFabricatedPunishment() {
	s.Run("removing the lying consumer cancels its fabricated punishment", func() {
		const consumerID = "0"
		val2 := s.validator(2)
		_, tokensBefore := s.stakingValidatorState(val2.valoper)
		s.Require().Equalf(2, s.pendingEquivocationsOf(consumerID, val2.consAddr),
			"val2's two fabricated punishments must still be pending before the removal")

		// The validator the forge accused of downtime must stand unaccused
		// before the removal, which would cancel any accusation still pending.
		val1 := s.validator(1)
		s.Require().Zerof(s.pendingSlashesOf(consumerID, val1.consAddr), "val1 is still accused of downtime")

		s.submitAndPassProposal(s.removalProposalJSON(consumerID, "Remove the lying consumer"))
		s.Require().Eventuallyf(func() bool {
			phase := s.queryProviderConsumerPhase(consumerID)
			return phase == phaseStopped || phase == phaseDeleted
		}, time.Minute, 2*time.Second, "the consumer was not removed after the vote passed")

		s.Require().Truef(s.pendingEquivocationsEmpty(consumerID), "the removal must cancel the consumer's pending punishments")
		_, tokensAfter := s.stakingValidatorState(val2.valoper)
		s.Require().Equalf(tokensBefore, tokensAfter, "val2 must never be slashed for the fabricated double-sign")
		s.Require().Falsef(s.signingInfoTombstoned(val2.consAddr), "val2 must never be tombstoned for the fabricated double-sign")

		s.unjailFrom(2, val2.valoper)

		// The suite's thesis, end to end: the validator the forge accused of
		// downtime lost nothing.
		_, val1Stake := s.stakingValidatorState(val1.valoper)
		s.Require().Equalf(s.val1StakeBeforeLie, val1Stake, "val1 lost stake to the lying consumer")
	})
}
