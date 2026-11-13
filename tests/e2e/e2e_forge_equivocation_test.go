package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// consensusPrivKey returns validator i's consensus private key, read from
// its provider node's priv_validator_key.json. Its consumer node signs with
// the same key, so this is the key a malicious consumer binary on that node
// would hold.
func (s *ByzantineConsumerIntegrationTestSuite) consensusPrivKey(i int) cmted25519.PrivKey {
	raw, err := os.ReadFile(filepath.Join(s.providerHome(i), "config", "priv_validator_key.json"))
	s.Require().NoErrorf(err, "failed to read provider val%d's consensus key", i)
	var keyFile struct {
		PrivKey struct {
			Value string `json:"value"`
		} `json:"priv_key"`
	}
	s.Require().NoErrorf(json.Unmarshal(raw, &keyFile), "failed to decode provider val%d's consensus key", i)
	priv, err := base64.StdEncoding.DecodeString(keyFile.PrivKey.Value)
	s.Require().NoErrorf(err, "failed to decode provider val%d's private key", i)
	return cmted25519.PrivKey(priv)
}

// pendingEquivocationsOf counts the consumer's pending equivocation
// punishments naming the validator at consAddr.
func (s *ByzantineConsumerIntegrationTestSuite) pendingEquivocationsOf(consumerID, consAddr string) int { //nolint:unparam // the suite drives one consumer; the helper stays generic
	n, err := s.tryPendingEquivocationsOf(consumerID, consAddr)
	s.Require().NoErrorf(err, "failed to count the pending equivocation punishments naming %s", consAddr)
	return n
}

// tryPendingEquivocationsOf is pendingEquivocationsOf returning an error
// instead of failing the test, for the conditions of Eventually and Never,
// which run in a goroutine of their own: a failure there stalls the wait to
// its timeout.
func (s *ByzantineConsumerIntegrationTestSuite) tryPendingEquivocationsOf(consumerID, consAddr string) (int, error) {
	pending, err := s.pendingEquivocationsNaming(consumerID, consAddr)
	return len(pending), err
}

// pendingEquivocationsNaming lists the consumer's pending equivocation
// punishments naming the validator at consAddr.
func (s *ByzantineConsumerIntegrationTestSuite) pendingEquivocationsNaming(consumerID, consAddr string) ([]pendingEquivocationJSON, error) {
	addr, err := sdk.ConsAddressFromBech32(consAddr)
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", consAddr, err)
	}
	pending, err := s.queryPendingEquivocations(consumerID)
	if err != nil {
		return nil, err
	}
	var naming []pendingEquivocationJSON
	for _, p := range pending {
		if bytes.Equal(p.ProviderConsAddr, addr) {
			naming = append(naming, p)
		}
	}
	return naming, nil
}

// pendingEquivocationMaturity returns when the consumer's one pending
// equivocation punishment naming the validator at consAddr executes.
func (s *ByzantineConsumerIntegrationTestSuite) pendingEquivocationMaturity(consumerID, consAddr string) time.Time { //nolint:unparam // the suite drives one consumer; the helper stays generic
	pending, err := s.pendingEquivocationsNaming(consumerID, consAddr)
	s.Require().NoErrorf(err, "failed to read the pending equivocation punishments naming %s", consAddr)
	s.Require().Lenf(pending, 1, "one pending equivocation punishment naming %s expected, got %d", consAddr, len(pending))
	return pending[0].ExecutesAt
}

// testFabricatedDoubleSignJailsAndDefers: the test signs two conflicting
// votes with val2's consumer key, which val2 never signed, and submits them
// as consumer double-voting evidence. The provider cannot tell them from a
// real double-sign: it jails val2 at once and queues the slash and the
// tombstone behind the execution delay, taking nothing yet.
func (s *ByzantineConsumerIntegrationTestSuite) testFabricatedDoubleSignJailsAndDefers() {
	s.Run("a fabricated double-sign jails the validator at once and defers its punishment", func() {
		const consumerID = "0"
		val2 := s.validator(2)
		_, tokensBefore := s.stakingValidatorState(val2.valoper)
		s.Require().Zero(s.pendingEquivocationsOf(consumerID, val2.consAddr), "val2 is already accused of a double-sign")

		h, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height")
		evidencePath, headerPath := s.fabricateDoubleVote(s.consensusPrivKey(2), s.cfg.consumerChainID, h-5, "forge-val2")
		s.submitDoubleVoteEvidence(consumerID, evidencePath, headerPath)

		s.Require().Eventuallyf(func() bool {
			jailed, _, err := s.tryStakingValidatorState(val2.valoper)
			return err == nil && jailed
		}, time.Minute, 2*time.Second, "val2 was not jailed for the fabricated double-sign")
		s.Require().Equalf(1, s.pendingEquivocationsOf(consumerID, val2.consAddr),
			"the fabricated double-sign must queue one punishment against val2")
		_, tokensAfter := s.stakingValidatorState(val2.valoper)
		s.Require().Equalf(tokensBefore, tokensAfter, "the punishment must be deferred, not executed")
		s.Require().Falsef(s.signingInfoTombstoned(val2.consAddr), "val2 must not be tombstoned yet")
	})
}
