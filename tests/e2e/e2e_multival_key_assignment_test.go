package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"time"
)

// assignConsumerKey assigns pubKeyJSON as validator i's consumer key on the
// consumer, signed by that validator's operator key in its own provider
// node, and waits for the transaction to commit.
func (s *MultiValidatorIntegrationTestSuite) assignConsumerKey(i int, pubKeyJSON string) {
	container := s.providerValRes[i].Container.ID
	stdout, stderr, err := s.dockerExec(container, []string{
		providerBinary, "tx", "vaasprovider", "assign-consensus-key", "0", pubKeyJSON,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "auto",
		"--gas-adjustment", "1.5",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to broadcast val%d's key assignment: stderr=%s", i, stderr.String())
	s.requireTxCommittedOn(container, providerBinary, providerHomePath, stdout.Bytes())
}

// installConsensusKey writes privValidatorKeyJSON as the priv_validator_key.json
// of a stopped node's home, for the node to sign under once it starts again.
func (s *MultiValidatorIntegrationTestSuite) installConsensusKey(home string, privValidatorKeyJSON []byte) {
	keyPath := filepath.Join(home, "config", "priv_validator_key.json")
	s.Require().NoErrorf(os.WriteFile(keyPath, privValidatorKeyJSON, 0o644), //nolint:gosec // the node runs as nonroot in its container and must read this bind-mounted file
		"failed to install a consensus key in %s", home)
}

// restartConsumerNodeWithKey stops consumer node i, installs the given
// priv_validator_key.json, and starts the node again under it.
func (s *MultiValidatorIntegrationTestSuite) restartConsumerNodeWithKey(i int, privValidatorKeyJSON []byte) {
	s.stopConsumerNode(i)
	s.installConsensusKey(s.consumerHome(i), privValidatorKeyJSON)
	s.startConsumerNode(i)
}

// restartProviderNodeWithKey is restartConsumerNodeWithKey for provider node i.
func (s *MultiValidatorIntegrationTestSuite) restartProviderNodeWithKey(i int, privValidatorKeyJSON []byte) {
	s.stopProviderNode(i)
	s.installConsensusKey(s.providerHome(i), privValidatorKeyJSON)
	s.startProviderNode(i)
}

// testKeyAssignmentWithNodeRestart: val3 assigns a consumer key the test
// generated and restarts its consumer node under it right after the
// assignment commits; the next epoch's VSC switches the consumer's set to
// the assigned address, the chain never stops, and val3 is accused of no
// downtime for the switch. What the scenario measures is the gap: the old
// address misses the blocks between the assignment and the switch, the new
// one the blocks the node is down for, and both stay under the window's
// missed-block bar only if the stop and the start are quick. An accusation
// against val3 would show as a pending slash naming its provider address,
// then as a drop of its stake; neither may appear.
func (s *MultiValidatorIntegrationTestSuite) testKeyAssignmentWithNodeRestart() {
	s.Run("a key assignment survives val3's node restarting under the new key", func() {
		const consumerID = "0"
		val3 := s.validator(3)
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens before the key assignment")
		pubKeyJSON, assigned, keyDoc := generateConsumerConsensusKeyPair(s.T())
		s.T().Logf("assigning val3 (%s) the consumer key %s", val3.consAddr, assigned)

		s.assignConsumerKey(3, pubKeyJSON)
		s.Require().Equalf(assigned, s.queryValidatorConsumerAddr(consumerID, val3.consAddr),
			"the provider does not report val3's assigned consumer address")

		s.restartConsumerNodeWithKey(3, keyDoc)

		s.Require().Eventuallyf(func() bool {
			addrs, err := s.consumerValsetConsAddrs()
			return err == nil && slices.Contains(addrs, assigned) && !slices.Contains(addrs, val3.consAddr)
		}, 4*time.Minute, 5*time.Second,
			"the consumer's set never switched val3 from %s to %s", val3.consAddr, assigned)

		// Over the next windows no accusation names val3, and the chain keeps
		// producing blocks under the switched set.
		loopStart, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the switch")
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			s.Require().Zerof(s.pendingSlashesOf(consumerID, val3.consAddr),
				"a downtime accusation names val3 (%s) after its key assignment", val3.consAddr)
			time.Sleep(5 * time.Second)
		}
		loopEnd, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the watch")
		s.Require().Greaterf(loopEnd, loopStart+30,
			"the consumer produced too few blocks under the switched set (%d to %d in two minutes)", loopStart, loopEnd)

		tokensAfter, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens after the key assignment")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"val3 was slashed across the key assignment: %s before, %s after", tokensBefore, tokensAfter)

		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during the key assignment", consumerID)
	})
}
