package e2e

import (
	"fmt"
	"time"
)

// testForgedDowntimeIsChallenged: every consumer node runs the forge, which
// reports val1 absent from sixty consecutive blocks it signed, a range that
// spans at least one full downtime window wherever it starts.
// The consumer accuses val1 and the provider queues the slash; a challenge
// built from the real chain, whose commits carry val1's signatures, cancels
// it, leaves val1's stake untouched, and pauses the consumer; governance
// resumes it once the lie has ended.
func (s *ByzantineConsumerIntegrationTestSuite) testForgedDowntimeIsChallenged() {
	s.Run("a forged downtime accusation against a live validator is challenged away", func() {
		const consumerID = "0"
		val1 := s.validator(1)
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val1.valoper)
		s.Require().NoError(err, "failed to read val1's stake before the lie")
		_, s.val1StakeBeforeLie = s.stakingValidatorState(val1.valoper)

		from, until := s.switchConsumerToForge(val1, 60)

		s.T().Log("waiting for the consumer's accusation of val1 to reach the provider...")
		s.Require().Eventuallyf(func() bool {
			return s.pendingSlashesOf(consumerID, val1.consAddr) > 0
		}, 6*time.Minute, 5*time.Second,
			"the forged absence never became a pending downtime slash against val1")

		var target downtimeChallengeTarget
		s.Require().Eventuallyf(func() bool {
			t, ok := s.findDowntimeChallengeTarget(consumerID)
			if ok && t.valoper == val1.valoper {
				target = t
				return true
			}
			return false
		}, 2*time.Minute, 5*time.Second,
			"no challengeable pending downtime slash against val1 appeared")
		// The block at height h tells the app who signed h-1, so the lie
		// covers the votes for heights from-1 to until-1.
		s.Require().Truef(target.claimedHeight >= from-1 && target.claimedHeight <= until-1,
			"the accusation to challenge claims height %d, outside the lie's votes %d..%d", target.claimedHeight, from-1, until-1)
		s.T().Logf("challenging the accusation of val1 at claimed height %d", target.claimedHeight)

		clientID := s.queryConsumerClientID(consumerID)
		trustedHeight, ok := s.pickChallengeTrustedHeight(clientID, target.claimedHeight+1)
		s.Require().Truef(ok, "no stored consensus state below consumer height %d is usable as a trusted height", target.claimedHeight+1)

		consumerRPC := "http://" + s.consumerValRes[0].Container.Name[1:] + ":26657"
		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "vaasprovider", "challenge-consumer-downtime",
			consumerID, target.consumerConsAddr, fmt.Sprintf("%d", target.claimedHeight),
			"--consumer-rpc", consumerRPC,
			"--trusted-height", fmt.Sprintf("%d", trustedHeight),
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--gas", "5000000",
			"--fees", "100000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to run challenge-consumer-downtime: stderr=%s", stderr.String())
		code, rawLog := s.queryTxResult(stdout.Bytes())
		s.Require().Zerof(code, "the challenge of a forged accusation must succeed: %s", rawLog)

		s.Require().Zerof(s.pendingSlashesOf(consumerID, val1.consAddr),
			"the challenged accusation of val1 is still pending")
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID),
			"a successful challenge must pause the lying consumer")
		tokensAfter, err := s.getProviderValidatorTokensByAddr(val1.valoper)
		s.Require().NoError(err, "failed to read val1's stake after the challenge")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"val1 lost stake to a forged accusation: %s before, %s after", tokensBefore, tokensAfter)

		// The last downtime window the lie touched (the one holding the vote
		// for until-1) closes at the block after its last vote; its
		// accusation is sent then. Resuming only once that block is past
		// means the accusation reaches a paused consumer's provider, which
		// rejects it, instead of racing the resume.
		const window = 30 // signed_blocks_window in patchFourValidatorProviderGenesis
		lastWindowClose := ((until-1)/window)*window + window
		s.T().Logf("waiting for the last window the lie touched to close (block %d) before resuming...", lastWindowClose)
		s.Require().Eventuallyf(func() bool {
			h, err := s.queryConsumerBlockHeight()
			return err == nil && h > lastWindowClose+5
		}, 3*time.Minute, 3*time.Second, "the consumer never got past block %d", lastWindowClose)

		s.submitAndPassProposal(s.resumeProposalJSON(consumerID, "Resume consumer (forged downtime challenged)"))
		s.Require().Eventuallyf(func() bool {
			return s.queryProviderConsumerPhase(consumerID) == phaseLaunched
		}, time.Minute, 2*time.Second, "the consumer did not resume after governance approval")
		s.waitForConsumerSync(consumerID)
	})
}
