package e2e

import (
	"bytes"
	"context"
	"time"

	tmtypes "github.com/cometbft/cometbft/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// rotateConsensusKey submits MsgRotateConsPubKey for validator i's operator,
// signed by its operator key in its own provider node, and returns the
// committed transaction's code and raw log. The gas is fixed rather than
// simulated so a rotation the provider refuses is committed and refused on
// chain, where its reason can be read back, instead of failing the
// simulation.
func (s *MultiValidatorIntegrationTestSuite) rotateConsensusKey(i int, valoper, pubKeyJSON string) (code int, rawLog string) {
	container := s.providerValRes[i].Container.ID
	stdout, stderr, err := s.dockerExec(container, []string{
		providerBinary, "tx", "staking", "rotate-cons-pubkey", valoper, pubKeyJSON,
		"--from", "val",
		"--home", providerHomePath,
		"--keyring-backend", "test",
		"--chain-id", s.cfg.providerChainID,
		"--gas", "500000",
		"--fees", "10000" + bondDenom,
		"-y",
		"-o", "json",
	})
	s.Require().NoErrorf(err, "failed to broadcast val%d's consensus key rotation: stderr=%s", i, stderr.String())
	return s.queryTxResult(stdout.Bytes())
}

// providerSignedLatestBlock reports whether the provider's latest block
// commit carries a commit signature from the consensus address.
func (s *MultiValidatorIntegrationTestSuite) providerSignedLatestBlock(consAddr string) bool {
	addr, err := sdk.ConsAddressFromBech32(consAddr)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := s.providerCometClient().Commit(ctx, nil)
	if err != nil || res.Commit == nil {
		return false
	}
	for _, sig := range res.Commit.Signatures {
		if sig.BlockIDFlag == tmtypes.BlockIDFlagCommit && bytes.Equal(sig.ValidatorAddress, addr.Bytes()) {
			return true
		}
	}
	return false
}

// providerPowerOf returns the voting power the provider's consensus set
// gives consAddr, and whether consAddr is in the set.
func (s *MultiValidatorIntegrationTestSuite) providerPowerOf(consAddr string) (int64, bool) {
	vals, err := s.queryProviderNetValidators()
	if err != nil {
		return 0, false
	}
	power, ok := s.valAddrPowerMap(vals)[consAddr]
	return power, ok
}

// requireProviderRotated waits until the provider's consensus set carries
// the validator under its rotated key at its power and no longer under the
// old one, then requires x/staking to report the validator under the
// rotated key, the validator to sign a provider block under it, and no jail
// for the restart in between.
func (s *MultiValidatorIntegrationTestSuite) requireProviderRotated(v validatorIdentity, rotated string, power int64) {
	s.Require().Eventuallyf(func() bool {
		now, ok := s.providerPowerOf(rotated)
		_, old := s.providerPowerOf(v.consAddr)
		return ok && now == power && !old
	}, 2*time.Minute, 3*time.Second,
		"the provider's consensus set never switched val%d from %s to %s at power %d", v.index, v.consAddr, rotated, power)
	s.Require().Equalf(rotated, s.providerValidatorConsAddr(v.valoper),
		"x/staking does not report val%d under its rotated key", v.index)

	s.Require().Eventuallyf(func() bool {
		return s.providerSignedLatestBlock(rotated)
	}, 2*time.Minute, 2*time.Second,
		"val%d never signed a provider block under its rotated key %s", v.index, rotated)
	jailed, _ := s.stakingValidatorState(v.valoper)
	s.Require().Falsef(jailed, "val%d was jailed across its key rotation", v.index)
}

// requireNoAccusationAfterRotation watches the consumer's pending downtime
// slashes for d and fails on any that names the validator, under its old or
// its rotated provider address, while requiring the consumer to keep
// producing blocks.
func (s *MultiValidatorIntegrationTestSuite) requireNoAccusationAfterRotation(consumerID string, v validatorIdentity, rotated string, d time.Duration) {
	start, err := s.queryConsumerBlockHeight()
	s.Require().NoError(err, "failed to read the consumer height before the watch")
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, addr := range []string{v.consAddr, rotated} {
			s.Require().Zerof(s.pendingSlashesOf(consumerID, addr),
				"a downtime accusation names val%d (%s) after its key rotation", v.index, addr)
		}
		time.Sleep(5 * time.Second)
	}
	end, err := s.queryConsumerBlockHeight()
	s.Require().NoError(err, "failed to read the consumer height after the watch")
	minBlocks := int64(d.Seconds()) / 4
	s.Require().Greaterf(end, start+minBlocks,
		"the consumer produced too few blocks after val%d's rotation (%d to %d in %s)", v.index, start, end, d)
}

// testConsensusKeyRotation: a validator rotates its provider consensus key
// (x/staking's MsgRotateConsPubKey) and restarts its provider node under the
// new key. The provider's consensus set switches to the key at the end of
// the block that applies the rotation, the validator signs again once its
// node is back and is not jailed for the gap (the provider's signed-blocks
// window is 100000 blocks wide in this suite, see SetupSuite), and what the
// consumer sees depends on the validator's key assignment:
//
//   - val3 validates the consumer under the key it assigned in
//     testKeyAssignmentWithNodeRestart, so its rotation changes nothing on
//     the consumer: the set keeps the assigned address at its power, the
//     provider carries the assignment over to the rotated address, and no
//     accusation names val3 across the restart.
//   - val1 validates the consumer under its provider key, so its rotation
//     reaches the consumer: the provider hands the consumer the rotated key
//     as soon as it applies the rotation, the consumer's set switches to it,
//     and val1's consumer node restarts under the new key to keep signing.
//
// In between, val0 tries to rotate onto val3's assigned consumer key, and
// the provider refuses the rotation before x/staking records it (see
// BeforeConsensusPubKeyRotation in x/vaas/provider/keeper/hooks.go): two
// validators would otherwise meet at one address on the consumer.
//
// x/staking allows a validator one rotation per unbonding period and charges
// a rotation fee the suite's genesis prices in the bond denom (see
// SetupSuite), so each validator rotates once.
func (s *MultiValidatorIntegrationTestSuite) testConsensusKeyRotation() {
	const consumerID = "0"

	s.Run("val3 rotates its provider consensus key and the consumer keeps seeing its assigned key", func() {
		val3 := s.validator(3)
		assignedPub, _ := nodeConsensusKey(s.T(), s.consumerHome(3))
		assigned := consAddrFromEd25519PubKey(assignedPub)
		s.Require().Equalf(assigned, s.queryValidatorConsumerAddr(consumerID, val3.consAddr),
			"val3 must validate the consumer under the key its consumer node runs, assigned in the key-assignment scenario")
		assignedPower, ok := s.consumerPowerOf(assigned)
		s.Require().Truef(ok, "val3's assigned key %s is not in the consumer's set", assigned)
		providerPower, ok := s.providerPowerOf(val3.consAddr)
		s.Require().Truef(ok, "val3 (%s) is not in the provider's consensus set", val3.consAddr)
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens before the rotation")

		pubKeyJSON, rotated, keyDoc := generateConsumerConsensusKeyPair(s.T())
		s.T().Logf("rotating val3 (%s) to the provider consensus key %s", val3.consAddr, rotated)
		code, rawLog := s.rotateConsensusKey(3, val3.valoper, pubKeyJSON)
		s.Require().Zerof(code, "val3's key rotation was refused: %s", rawLog)
		s.restartProviderNodeWithKey(3, keyDoc)
		s.requireProviderRotated(val3, rotated, providerPower)

		// The assignment moves with the validator: the rotated address holds
		// it, the old one no longer does, and the assigned key resolves back
		// to the rotated address.
		s.Require().Equalf(assigned, s.queryValidatorConsumerAddr(consumerID, rotated),
			"the provider does not carry val3's key assignment over to its rotated address")
		s.Require().Emptyf(s.queryValidatorConsumerAddr(consumerID, val3.consAddr),
			"val3's key assignment is still kept under its old address")
		s.Require().Equalf(rotated, s.queryValidatorProviderAddr(consumerID, assigned),
			"val3's assigned key does not resolve to its rotated address")

		// Over the next epochs the consumer keeps the assigned address at its
		// power and never shows val3's old or rotated provider key.
		s.Require().Never(func() bool {
			vals, err := s.queryConsumerNetValidators()
			if err != nil {
				return false
			}
			powers := s.valAddrPowerMap(vals)
			_, old := powers[val3.consAddr]
			_, now := powers[rotated]
			return powers[assigned] != assignedPower || old || now
		}, 30*time.Second, 5*time.Second,
			"the consumer's view of val3 changed with its provider key rotation")

		s.requireNoAccusationAfterRotation(consumerID, val3, rotated, 60*time.Second)
		tokensAfter, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens after the rotation")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"val3 was slashed across its key rotation: %s before, %s after", tokensBefore, tokensAfter)
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during val3's key rotation", consumerID)
	})

	s.Run("a rotation onto val3's assigned consumer key is refused", func() {
		val0 := s.validator(0)
		assignedPub, _ := nodeConsensusKey(s.T(), s.consumerHome(3))
		s.T().Logf("val0 (%s) tries to rotate onto val3's assigned consumer key %s",
			val0.consAddr, consAddrFromEd25519PubKey(assignedPub))
		code, rawLog := s.rotateConsensusKey(0, val0.valoper, ed25519PubKeyJSON(assignedPub))
		s.Require().NotZerof(code, "val0's rotation onto val3's assigned consumer key was admitted")
		s.Require().Containsf(rawLog, "already assigned as another validator's consumer key",
			"val0's rotation was refused for another reason: %s", rawLog)
		s.Require().Equalf(val0.consAddr, s.providerValidatorConsAddr(val0.valoper),
			"the refused rotation changed val0's consensus key")
	})

	s.Run("val1 rotates its provider consensus key and the consumer's set switches to it", func() {
		val1 := s.validator(1)
		s.Require().Emptyf(s.queryValidatorConsumerAddr(consumerID, val1.consAddr),
			"val1 must validate the consumer under its provider key, with no assigned consumer key")
		consumerPower, ok := s.consumerPowerOf(val1.consAddr)
		s.Require().Truef(ok, "val1 (%s) is not in the consumer's set", val1.consAddr)
		providerPower, ok := s.providerPowerOf(val1.consAddr)
		s.Require().Truef(ok, "val1 (%s) is not in the provider's consensus set", val1.consAddr)
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val1.valoper)
		s.Require().NoError(err, "failed to read val1's tokens before the rotation")

		pubKeyJSON, rotated, keyDoc := generateConsumerConsensusKeyPair(s.T())
		s.T().Logf("rotating val1 (%s) to the provider consensus key %s", val1.consAddr, rotated)
		code, rawLog := s.rotateConsensusKey(1, val1.valoper, pubKeyJSON)
		s.Require().Zerof(code, "val1's key rotation was refused: %s", rawLog)

		// The consumer learns the rotated key as soon as the provider applies
		// the rotation, so the consumer node moves first and quickly: the old
		// address misses the blocks until the switch, the new one those until
		// the node is back, and both must stay under the downtime window's
		// bar. The provider node follows; its window is wide.
		s.restartConsumerNodeWithKey(1, keyDoc)
		s.restartProviderNodeWithKey(1, keyDoc)

		// The provider queues the key change at the next epoch boundary and
		// the relayer delivers it; the bound checked is three minutes from
		// both nodes being back, which leaves room for a slow relayer.
		s.Require().Eventuallyf(func() bool {
			power, ok := s.consumerPowerOf(rotated)
			_, old := s.consumerPowerOf(val1.consAddr)
			return ok && power == consumerPower && !old
		}, 3*time.Minute, 3*time.Second,
			"the consumer's set never switched val1 from %s to %s at power %d", val1.consAddr, rotated, consumerPower)
		s.requireProviderRotated(val1, rotated, providerPower)

		s.requireNoAccusationAfterRotation(consumerID, val1, rotated, 90*time.Second)
		tokensAfter, err := s.getProviderValidatorTokensByAddr(val1.valoper)
		s.Require().NoError(err, "failed to read val1's tokens after the rotation")
		s.Require().Truef(tokensAfter.Equal(tokensBefore),
			"val1 was slashed across its key rotation: %s before, %s after", tokensBefore, tokensAfter)
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase during val1's key rotation", consumerID)
	})
}
