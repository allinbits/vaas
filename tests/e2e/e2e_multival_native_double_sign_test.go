package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	tmtypes "github.com/cometbft/cometbft/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// fabricateProviderDoubleVote signs two conflicting precommits for the
// provider block at height with a validator's own provider consensus key and
// builds the DuplicateVoteEvidence the provider's CometBFT verifies against
// its own state: the validator set at that height, which must hold the key
// at the power the evidence claims, and the block's time, which the evidence
// must carry.
func (s *MultiValidatorIntegrationTestSuite) fabricateProviderDoubleVote(priv cmted25519.PrivKey, height int64) *tmtypes.DuplicateVoteEvidence {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := s.providerCometClient()

	perPage := 100
	valsRes, err := client.Validators(ctx, &height, nil, &perPage)
	s.Require().NoErrorf(err, "failed to read the provider's validator set at height %d", height)
	valSet := tmtypes.NewValidatorSet(valsRes.Validators)
	index, val := valSet.GetByAddress(priv.PubKey().Address())
	s.Require().NotNilf(val, "the double signer %X was not in the provider's set at height %d", priv.PubKey().Address(), height)

	blockRes, err := client.Block(ctx, &height)
	s.Require().NoErrorf(err, "failed to read the provider block at height %d", height)
	blockTime := blockRes.Block.Time

	protoA, protoB := s.signConflictingPrecommits(priv, s.cfg.providerChainID, height, index, blockTime)
	voteA, err := tmtypes.VoteFromProto(protoA)
	s.Require().NoError(err, "failed to convert the first fabricated vote")
	voteB, err := tmtypes.VoteFromProto(protoB)
	s.Require().NoError(err, "failed to convert the second fabricated vote")

	evidence, err := tmtypes.NewDuplicateVoteEvidence(voteA, voteB, blockTime, valSet)
	s.Require().NoError(err, "failed to build the duplicate vote evidence")
	s.Require().NoError(evidence.ValidateBasic(), "the fabricated duplicate vote evidence is malformed")
	return evidence
}

// broadcastProviderEvidence hands evidence to provider val0's CometBFT
// through its RPC, as a peer that observed the double sign would. The node
// verifies it against its own state before admitting it to its evidence
// pool, from where a proposer picks it up.
func (s *MultiValidatorIntegrationTestSuite) broadcastProviderEvidence(evidence tmtypes.Evidence) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.providerCometClient().BroadcastEvidence(ctx, evidence)
	s.Require().NoError(err, "the provider's CometBFT rejected the fabricated double-vote evidence")
	s.T().Logf("the provider's evidence pool admitted the double-vote evidence %X", res.Hash)
}

// providerBlockCarriesDoubleVoteOf reports whether the provider block at
// height carries duplicate vote evidence against the consensus address.
func (s *MultiValidatorIntegrationTestSuite) providerBlockCarriesDoubleVoteOf(height int64, addr []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := s.providerCometClient().Block(ctx, &height)
	if err != nil || res.Block == nil {
		return false
	}
	for _, ev := range res.Block.Evidence.Evidence {
		if dve, ok := ev.(*tmtypes.DuplicateVoteEvidence); ok && bytes.Equal(dve.VoteA.ValidatorAddress, addr) {
			return true
		}
	}
	return false
}

// providerSlashFractionDoubleSign reads x/slashing's double-sign fraction on
// the provider, what a native double sign costs. The CLI prints the params
// bare or under a "params" envelope; both are read.
func (s *MultiValidatorIntegrationTestSuite) providerSlashFractionDoubleSign() math.LegacyDec {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "slashing", "params",
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query the provider's slashing params: %s", stderr.String())
	var res struct {
		SlashFractionDoubleSign string `json:"slash_fraction_double_sign"`
		Params                  struct {
			SlashFractionDoubleSign string `json:"slash_fraction_double_sign"`
		} `json:"params"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res),
		"undecodable slashing params: %s", stdout.String())
	raw := res.SlashFractionDoubleSign
	if raw == "" {
		raw = res.Params.SlashFractionDoubleSign
	}
	fraction, err := math.LegacyNewDecFromStr(raw)
	s.Require().NoErrorf(err, "unparsable slash_fraction_double_sign %q in %s", raw, stdout.String())
	return fraction
}

// testNativeDoubleSignIsPunished: val3 double-signs a provider block, the
// infraction the provider's own CometBFT reports to the application as
// misbehaviour, with no VAAS message involved. The test plays the node that
// caught it: it signs two conflicting precommits for a recent provider block
// with val3's provider consensus key, what a node holding the key could
// produce, and hands them to provider val0's CometBFT, which verifies them
// against its own state. A proposer includes them in a block, and x/evidence
// punishes val3 in that block: x/slashing's double-sign fraction off its
// stake, jailed, tombstoned. The provider's consensus set drops val3, which
// the VAAS provider module carries to CometBFT itself since x/staking on the
// provider pushes no updates; the next epoch's VSC drops val3 from the
// consumer's set; and no VAAS equivocation punishment is queued, those being
// for double signs on a consumer. The tombstone is for good, so this is the
// suite's last scenario.
func (s *MultiValidatorIntegrationTestSuite) testNativeDoubleSignIsPunished() {
	s.Run("a double sign on the provider is slashed, jailed and tombstoned through x/evidence", func() {
		const consumerID = "0"
		val3 := s.validator(3)
		_, priv := nodeConsensusKey(s.T(), s.providerHome(3))
		addr := priv.PubKey().Address().Bytes()
		s.Require().Equalf(val3.consAddr, sdk.ConsAddress(addr).String(),
			"provider val3's key file does not match the key the suite resolved it by")

		// The consumer sees val3 under its assigned key when it has one.
		consumerAddr := s.queryValidatorConsumerAddr(consumerID, val3.consAddr)
		if consumerAddr == "" {
			consumerAddr = val3.consAddr
		}
		_, ok := s.consumerPowerOf(consumerAddr)
		s.Require().Truef(ok, "val3 (%s on the consumer) is not in the consumer's set", consumerAddr)
		jailed, _ := s.stakingValidatorState(val3.valoper)
		s.Require().False(jailed, "val3 must not be jailed before its double sign")
		s.Require().False(s.signingInfoTombstoned(val3.consAddr), "val3 must not be tombstoned before its double sign")
		pending, err := s.queryPendingEquivocations(consumerID)
		s.Require().NoError(err, "failed to read the pending equivocation punishments before the double sign")
		s.Require().Empty(pending, "a VAAS equivocation punishment is pending before the double sign")
		tokensBefore, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens before the double sign")

		latest, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		height := latest - 2
		evidence := s.fabricateProviderDoubleVote(priv, height)
		s.T().Logf("val3 (%s) double-signed provider block %d; handing the evidence to val0...", val3.consAddr, height)
		s.broadcastProviderEvidence(evidence)

		next := latest + 1
		var included int64
		s.Require().Eventuallyf(func() bool {
			head, err := s.queryProviderBlockHeight()
			if err != nil {
				return false
			}
			for ; next <= head; next++ {
				if s.providerBlockCarriesDoubleVoteOf(next, addr) {
					included = next
					return true
				}
			}
			return false
		}, 2*time.Minute, 2*time.Second,
			"no provider block after %d included val3's double-vote evidence", latest)
		s.T().Logf("provider block %d carries val3's double-vote evidence", included)

		// x/evidence punishes in the BeginBlock of the block that carries the
		// evidence: from that height val3 is jailed and tombstoned, with the
		// double-sign fraction of the stake behind its power at the
		// infraction height burnt.
		s.Require().Eventuallyf(func() bool {
			jailed, _ := s.stakingValidatorState(val3.valoper)
			return jailed && s.signingInfoTombstoned(val3.consAddr)
		}, time.Minute, 2*time.Second,
			"val3 was not jailed and tombstoned for its double sign")

		fraction := s.providerSlashFractionDoubleSign()
		slashed := math.LegacyNewDecFromInt(
			sdk.TokensFromConsensusPower(evidence.ValidatorPower, sdk.DefaultPowerReduction),
		).Mul(fraction).TruncateInt()
		tokensAfter, err := s.getProviderValidatorTokensByAddr(val3.valoper)
		s.Require().NoError(err, "failed to read val3's tokens after the double sign")
		s.Require().Truef(tokensAfter.Equal(tokensBefore.Sub(slashed)),
			"val3's stake after the double sign is %s, want %s less the %s slash of %s", tokensAfter, tokensBefore, fraction, slashed)

		s.Require().Eventuallyf(func() bool {
			_, ok := s.providerPowerOf(val3.consAddr)
			return !ok
		}, time.Minute, 2*time.Second,
			"the provider's consensus set kept val3 (%s) after it was jailed", val3.consAddr)
		s.Require().Eventuallyf(func() bool {
			_, ok := s.consumerPowerOf(consumerAddr)
			return !ok
		}, 3*time.Minute, 3*time.Second,
			"the consumer's set kept val3 (%s) after the provider jailed it", consumerAddr)
		addrs, err := s.consumerValsetConsAddrs()
		s.Require().NoError(err, "failed to query the consumer's validator set")
		s.Require().Lenf(addrs, multivalValidators-1, "the consumer's set is not the three remaining validators: %v", addrs)

		// Both chains go on with three validators, and the provider never
		// turns the native punishment into a VAAS one.
		consumerStart, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after val3's removal")
		s.Require().Never(func() bool {
			pending, err := s.queryPendingEquivocations(consumerID)
			return err == nil && len(pending) > 0
		}, 20*time.Second, 4*time.Second,
			"a VAAS equivocation punishment was queued for a double sign on the provider")
		consumerEnd, err := s.queryConsumerBlockHeight()
		s.Require().NoError(err, "failed to read the consumer height after the watch")
		s.Require().Greaterf(consumerEnd, consumerStart+5,
			"the consumer produced too few blocks without val3 (%d to %d in 20s)", consumerStart, consumerEnd)
		providerEnd, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height after the watch")
		s.Require().Greaterf(providerEnd, included+5,
			"the provider produced too few blocks without val3 (%d to %d)", included, providerEnd)
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID),
			"consumer %s left the LAUNCHED phase over val3's double sign", consumerID)
	})
}
