package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	cometversion "github.com/cometbft/cometbft/version"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
)

// consumerValidatorSetAt reads the consumer's validator set at height from
// its own RPC and rebuilds it as a CometBFT validator set (its hash depends
// only on the keys and powers).
func (s *ByzantineConsumerIntegrationTestSuite) consumerValidatorSetAt(height int64) *tmtypes.ValidatorSet {
	body, err := httpGet(fmt.Sprintf("%s/validators?height=%d&per_page=100", s.consumerRPCEndpoint(), height))
	s.Require().NoErrorf(err, "failed to read the consumer's validators at height %d", height)
	var res struct {
		Result struct {
			Validators []struct {
				PubKey struct {
					Value string `json:"value"`
				} `json:"pub_key"`
				VotingPower string `json:"voting_power"`
			} `json:"validators"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &res), "failed to decode the consumer's validators: %s", body)
	vals := make([]*tmtypes.Validator, 0, len(res.Result.Validators))
	for _, v := range res.Result.Validators {
		pub, err := base64.StdEncoding.DecodeString(v.PubKey.Value)
		s.Require().NoError(err, "failed to decode a consumer validator's public key")
		power, err := strconv.ParseInt(v.VotingPower, 10, 64)
		s.Require().NoError(err, "failed to parse a consumer validator's power")
		vals = append(vals, tmtypes.NewValidator(cmted25519.PubKey(pub), power))
	}
	return tmtypes.NewValidatorSet(vals)
}

// consumerBlockTime reads the time of the consumer's block at height.
func (s *ByzantineConsumerIntegrationTestSuite) consumerBlockTime(height int64) time.Time {
	body, err := httpGet(fmt.Sprintf("%s/block?height=%d", s.consumerRPCEndpoint(), height))
	s.Require().NoErrorf(err, "failed to read the consumer's block %d", height)
	var res struct {
		Result struct {
			Block struct {
				Header struct {
					Time time.Time `json:"time"`
				} `json:"header"`
			} `json:"block"`
		} `json:"result"`
	}
	s.Require().NoErrorf(json.Unmarshal(body, &res), "failed to decode the consumer's block %d", height)
	return res.Result.Block.Header.Time
}

// fabricateFork builds a light-client attack on the consumer: two headers at
// the same height, differing in their app hash, each with a full commit
// signed by every validator of the consumer's set, both verifying against a
// consensus state the provider's client of the consumer stores. It writes
// the ibc-go misbehaviour into val0's provider container and returns its
// path there.
func (s *ByzantineConsumerIntegrationTestSuite) fabricateFork(clientID string) string {
	head, err := s.queryConsumerBlockHeight()
	s.Require().NoError(err, "failed to read the consumer height")
	trusted, ok := s.pickChallengeTrustedHeight(clientID, head)
	s.Require().Truef(ok, "no stored consensus state of client %s has a stable validator set below height %d", clientID, head)
	height := trusted + 1

	valset := s.consumerValidatorSetAt(height)
	valsetProto, err := valset.ToProto()
	s.Require().NoError(err, "failed to encode the consumer's validator set")
	keys := map[string]cmted25519.PrivKey{}
	for i := range s.validatorCount() {
		priv := s.consensusPrivKey(i)
		keys[string(priv.PubKey().Address())] = priv
	}

	chainID := s.cfg.consumerChainID
	stamp := s.consumerBlockTime(height)
	revision := clienttypes.ParseChainID(chainID)

	makeHeader := func(appHashByte byte) *ibctmtypes.Header {
		header := tmtypes.Header{
			Version:            cmtversion.Consensus{Block: cometversion.BlockProtocol},
			ChainID:            chainID,
			Height:             height,
			Time:               stamp,
			ValidatorsHash:     valset.Hash(),
			NextValidatorsHash: valset.Hash(),
			AppHash:            bytes.Repeat([]byte{appHashByte}, 32),
			ProposerAddress:    valset.Proposer.Address,
		}
		blockID := tmtypes.BlockID{
			Hash:          header.Hash(),
			PartSetHeader: tmtypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{appHashByte ^ 0xFF}, 32)},
		}
		sigs := make([]tmtypes.CommitSig, len(valset.Validators))
		for idx, v := range valset.Validators {
			priv, ok := keys[string(v.Address)]
			s.Require().Truef(ok, "the suite holds no key for consumer validator %s", hex.EncodeToString(v.Address))
			vote := &tmproto.Vote{
				Type:             tmproto.PrecommitType,
				Height:           height,
				Round:            0,
				BlockID:          blockID.ToProto(),
				Timestamp:        stamp,
				ValidatorAddress: v.Address,
				ValidatorIndex:   int32(idx),
			}
			sig, err := priv.Sign(tmtypes.VoteSignBytes(chainID, vote))
			s.Require().NoError(err, "failed to sign a fork vote")
			sigs[idx] = tmtypes.CommitSig{BlockIDFlag: tmtypes.BlockIDFlagCommit, ValidatorAddress: v.Address, Timestamp: stamp, Signature: sig}
		}
		commit := &tmtypes.Commit{Height: height, Round: 0, BlockID: blockID, Signatures: sigs}
		signed := tmtypes.SignedHeader{Header: &header, Commit: commit}
		return &ibctmtypes.Header{
			SignedHeader:      signed.ToProto(),
			ValidatorSet:      valsetProto,
			TrustedHeight:     clienttypes.NewHeight(revision, uint64(trusted)),
			TrustedValidators: valsetProto,
		}
	}

	misbehaviour := &ibctmtypes.Misbehaviour{ClientId: clientID, Header1: makeHeader(0xA1), Header2: makeHeader(0xB2)}
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	misbehaviourJSON, err := cdc.MarshalJSON(misbehaviour)
	s.Require().NoError(err, "failed to marshal the fabricated misbehaviour")

	path := "/tmp/misbehaviour_fork.json"
	s.dockerExecMust(s.providerValRes[0].Container.ID, []string{
		"sh", "-c", fmt.Sprintf("echo %s | base64 -d > %s", base64.StdEncoding.EncodeToString(misbehaviourJSON), path),
	})
	s.T().Logf("fabricated a fork of the consumer at height %d over trusted height %d", height, trusted)
	return path
}

// clientStatus reads the status of one of the provider's IBC clients.
func (s *ByzantineConsumerIntegrationTestSuite) clientStatus(clientID string) string {
	status, err := s.tryClientStatus(clientID)
	s.Require().NoErrorf(err, "failed to read the status of client %s", clientID)
	return status
}

// tryClientStatus is clientStatus returning an error instead of failing the
// test, for the conditions of Eventually and Never, which run in a goroutine
// of their own: a failure there stalls the wait to its timeout.
func (s *ByzantineConsumerIntegrationTestSuite) tryClientStatus(clientID string) (string, error) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "client", "status", clientID,
		"--home", providerHomePath,
		"--output", "json",
	})
	if err != nil {
		return "", fmt.Errorf("querying the status of client %s: %w (stderr=%s)", clientID, err, stderr.String())
	}
	var res struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return "", fmt.Errorf("decoding the status of client %s: %w (raw=%s)", clientID, err, stdout.String())
	}
	return res.Status, nil
}

// committedTxHeight returns the height of the provider block that committed
// the transaction a `-o json` broadcast response names, once
// requireTxCommitted has seen it committed.
func (s *ByzantineConsumerIntegrationTestSuite) committedTxHeight(broadcastOut []byte) int64 {
	var bres struct {
		TxHash string `json:"txhash"`
	}
	s.Require().NoErrorf(json.Unmarshal(broadcastOut, &bres), "decode broadcast response: %s", string(broadcastOut))
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "tx", bres.TxHash,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query tx %s: %s", bres.TxHash, stderr.String())
	var res struct {
		Height string `json:"height"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode tx %s: %s", bres.TxHash, stdout.String())
	height, err := strconv.ParseInt(res.Height, 10, 64)
	s.Require().NoErrorf(err, "tx %s height: %q", bres.TxHash, res.Height)
	return height
}

// testForkFreezesAndPausesWithoutPunishing: the test signs a fork of the
// consumer's history with every validator's key and submits it as
// light-client misbehaviour. The provider freezes its client of the consumer
// and pauses the consumer, and takes nothing from anyone; a governance
// resume fails, since the consumer's client is no longer active.
func (s *ByzantineConsumerIntegrationTestSuite) testForkFreezesAndPausesWithoutPunishing() {
	s.Run("a fork freezes the consumer's client and pauses it, punishing nobody", func() {
		const consumerID = "0"
		clientID := s.queryConsumerClientID(consumerID)

		// Punishment comes in four forms here: a jail, a slash, a tombstone,
		// and a deferred punishment queued for later (an equivocation entry or
		// a downtime accusation), which moves no stake when it is queued. The
		// fork must add none of them.
		type state struct {
			jailed     bool
			tokens     string
			tombstoned bool
		}
		before := map[int]state{}
		for i := range s.validatorCount() {
			v := s.validator(i)
			jailed, tokens := s.stakingValidatorState(v.valoper)
			before[i] = state{jailed, tokens, s.signingInfoTombstoned(v.consAddr)}
		}
		equivocationsBefore := len(s.mustPendingEquivocations(consumerID))
		downtimeBefore := len(s.queryPendingDowntimeSlashes(consumerID))
		// val2's deferred punishment, queued by the fabricated double-sign,
		// keeps its maturity through the pause; the fork drill requires the
		// resume to move it by exactly the pause.
		val2 := s.validator(2)
		s.val2MaturityBeforeFork = s.pendingEquivocationMaturity(consumerID, val2.consAddr)

		path := s.fabricateFork(clientID)
		stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
			providerBinary, "tx", "vaasprovider", "submit-consumer-misbehaviour", consumerID, path,
			"--from", "val",
			"--home", providerHomePath,
			"--keyring-backend", "test",
			"--chain-id", s.cfg.providerChainID,
			"--gas", "5000000",
			"--fees", "100000" + bondDenom,
			"-y",
			"-o", "json",
		})
		s.Require().NoErrorf(err, "failed to submit the fabricated misbehaviour: stderr=%s", stderr.String())
		s.requireTxCommitted(stdout.Bytes())
		// The message freezes the client and pauses the consumer in its own
		// block, whose time is the pause's.
		s.forkPausedAt = s.providerBlockTime(s.committedTxHeight(stdout.Bytes()))

		s.Require().Equalf("Frozen", s.clientStatus(clientID), "the fork must freeze client %s", clientID)
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "the fork must pause the consumer")
		s.Require().Truef(s.pendingEquivocationMaturity(consumerID, val2.consAddr).Equal(s.val2MaturityBeforeFork),
			"the pause must leave val2's deferred punishment at its maturity")
		for i, was := range before {
			v := s.validator(i)
			jailed, tokens := s.stakingValidatorState(v.valoper)
			s.Require().Equalf(was.jailed, jailed, "the fork changed val%d's jail", i)
			s.Require().Equalf(was.tokens, tokens, "the fork changed val%d's stake", i)
			s.Require().Equalf(was.tombstoned, s.signingInfoTombstoned(v.consAddr), "the fork changed val%d's tombstone", i)
		}
		s.Require().Equalf(equivocationsBefore, len(s.mustPendingEquivocations(consumerID)),
			"the fork must queue no equivocation punishment")
		// The pause cancels the consumer's pending downtime accusations, so
		// their count may only fall.
		s.Require().LessOrEqualf(len(s.queryPendingDowntimeSlashes(consumerID)), downtimeBefore,
			"the fork must queue no downtime accusation")

		proposalID := s.submitProposalWithVote(s.resumeProposalJSON(consumerID, "Resume consumer (forked)"), "yes", "PROPOSAL_STATUS_FAILED")
		// An expired client would refuse the resume too; the status in the
		// reason shows it is the fork's freeze.
		failure := s.dumpProposal(proposalID)
		s.Require().Truef(strings.Contains(failure, "consumer client is not active") && strings.Contains(failure, "Frozen"),
			"the resume must fail because the client is frozen: %s", failure)
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "a refused resume must leave the consumer paused")
	})
}

// testForkRecoveryWaitsOutTheTrustingPeriod is the fork drill: the way back
// from the pause the fork caused, in the one order the protocol allows.
// Governance recovers the frozen client onto a substitute the relayer
// created and resumes the consumer in the same proposal; inside the client's
// trusting period the resume is refused (the fork's headers could still be
// verified against the recovered client and would pause the consumer
// again), the proposal fails, and the recovery is rolled back with it, so
// the client stays frozen. Once the trusting period has elapsed since the
// pause the same proposal passes: the client is active and the relayer
// keeps updating it with the consumer's real headers, the consumer is
// launched and its acks flow again, nobody was punished, and val2's deferred
// punishment executes as long after the resume as it would have after the
// pause. The forge suite's consumer carries a short unbonding period so this
// wait fits the job (create_consumer_forge.json).
func (s *ByzantineConsumerIntegrationTestSuite) testForkRecoveryWaitsOutTheTrustingPeriod() {
	s.Run("a recovery and resume is refused inside the trusting period, and passes once it has elapsed", func() {
		const consumerID = "0"
		subject := s.queryConsumerClientID(consumerID)
		s.Require().Equalf("Frozen", s.clientStatus(subject), "the drill starts from the fork's freeze")
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "the drill starts from the fork's pause")
		s.Require().Falsef(s.forkPausedAt.IsZero(), "the drill starts from the pause the fork scenario recorded")
		val2 := s.validator(2)

		type state struct {
			jailed     bool
			tokens     string
			tombstoned bool
		}
		before := map[int]state{}
		for i := range s.validatorCount() {
			v := s.validator(i)
			jailed, tokens := s.stakingValidatorState(v.valoper)
			before[i] = state{jailed, tokens, s.signingInfoTombstoned(v.consAddr)}
		}

		substitute := s.newProviderClientOfTheConsumer()
		s.T().Logf("recovering the frozen client %s onto the substitute %s", subject, substitute)
		proposal := s.recoverAndResumeProposalJSON(consumerID, subject, substitute)

		proposalID := s.submitProposalWithVote(proposal, "yes", "PROPOSAL_STATUS_FAILED")
		failure := s.dumpProposal(proposalID)
		s.Require().Containsf(failure, "trusting period", "the resume must be refused inside the trusting period: %s", failure)
		s.Require().Equalf("Frozen", s.clientStatus(subject), "a failed proposal rolls the recovery back with it")
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "a refused resume leaves the consumer paused")
		s.Require().Truef(s.pendingEquivocationMaturity(consumerID, val2.consAddr).Equal(s.val2MaturityBeforeFork),
			"a refused resume leaves val2's deferred punishment at its maturity")

		resumableAt := resumableAtFromFailure(failure)
		s.Require().Falsef(resumableAt.IsZero(), "the refusal must say when the resume is allowed: %s", failure)
		s.T().Logf("waiting until the fork's headers can no longer be verified, at %s (%s from now)...",
			resumableAt.Format(time.RFC3339), time.Until(resumableAt).Round(time.Second))
		// The chains run on the host's clock; a margin covers the block the
		// proposal executes in.
		s.Require().Eventuallyf(func() bool { return time.Now().After(resumableAt.Add(15 * time.Second)) },
			20*time.Minute, 5*time.Second, "the trusting period never elapsed")

		// The running relayer serves the paths it started with, so the first
		// substitute has gone unupdated through the wait and expired with
		// its own trusting period; the recovery takes a fresh one, created
		// moments before the proposal, as an operator's would be.
		substitute = s.newProviderClientOfTheConsumer()
		s.T().Logf("recovering the frozen client %s onto the fresh substitute %s", subject, substitute)
		proposal = s.recoverAndResumeProposalJSON(consumerID, subject, substitute)
		resumeSubmittedAt, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		s.submitProposalWithVote(proposal, "yes", "PROPOSAL_STATUS_PASSED")
		s.Require().Equalf("Active", s.clientStatus(subject), "the recovered client must be active")
		s.Require().Equalf(phaseLaunched, s.queryProviderConsumerPhase(consumerID), "the consumer must be launched again")

		// The resume thaws what the pause froze: val2's deferred punishment
		// executes as long after the resume as it would have after the
		// pause, the pause being the time from the block the fork paused the
		// consumer in to the block the proposal resumed it in.
		latest, err := s.queryProviderBlockHeight()
		s.Require().NoError(err, "failed to read the provider height")
		resumed := s.providerBlockEventHeights(resumeSubmittedAt, latest, resumedEventType, map[string]string{"consumer_id": consumerID})
		s.Require().Lenf(resumed, 1, "one resume of consumer %s expected in provider blocks %d..%d, got %v", consumerID, resumeSubmittedAt, latest, resumed)
		pausedFor := s.providerBlockTime(resumed[0]).Sub(s.forkPausedAt)
		wantMaturity := s.val2MaturityBeforeFork.Add(pausedFor)
		maturity := s.pendingEquivocationMaturity(consumerID, val2.consAddr)
		s.Require().Truef(maturity.Equal(wantMaturity),
			"val2's deferred punishment must execute at %s, its maturity before the fork moved by the pause of %s, not at %s",
			wantMaturity.Format(time.RFC3339Nano), pausedFor, maturity.Format(time.RFC3339Nano))
		s.T().Logf("the resume moved val2's deferred punishment by the pause of %s", pausedFor.Round(time.Second))

		// The recovered client verifies the consumer's real headers: the
		// relayer keeps moving it.
		recoveredAt := s.clientLatestHeight(subject)
		s.Require().Eventuallyf(func() bool {
			height, err := s.tryClientLatestHeight(subject)
			return err == nil && height > recoveredAt
		}, 3*time.Minute, 3*time.Second, "the recovered client %s was never updated past height %d", subject, recoveredAt)
		// And the consumer is served again: its acks flow.
		s.waitForConsumerSync(consumerID)

		for i, was := range before {
			v := s.validator(i)
			jailed, tokens := s.stakingValidatorState(v.valoper)
			s.Require().Equalf(was.jailed, jailed, "the drill changed val%d's jail", i)
			s.Require().Equalf(was.tokens, tokens, "the drill changed val%d's stake", i)
			s.Require().Equalf(was.tombstoned, s.signingInfoTombstoned(v.consAddr), "the drill changed val%d's tombstone", i)
		}
	})
}

// newProviderClientOfTheConsumer has the relayer create a fresh client pair
// between the chains and returns the provider-side one: a substitute for a
// client recovery, tracking the consumer from now.
func (s *ByzantineConsumerIntegrationTestSuite) newProviderClientOfTheConsumer() string {
	known := map[int]bool{}
	for _, p := range s.tsRelayerDumpPaths() {
		known[p.ID] = true
	}
	s.tsRelayerAddPath(IBCv2)
	for _, p := range s.tsRelayerDumpPaths() {
		if known[p.ID] {
			continue
		}
		switch s.cfg.providerChainID {
		case p.ChainIdA:
			return p.ClientA
		case p.ChainIdB:
			return p.ClientB
		}
	}
	s.Require().Fail("the relayer created no new path between the chains")
	return ""
}

// recoverAndResumeProposalJSON builds the proposal an operator submits to
// bring a consumer back from a fork: ibc-go's MsgRecoverClient onto the
// substitute, then MsgResumeConsumer, in one proposal so neither lands
// without the other.
func (s *ByzantineConsumerIntegrationTestSuite) recoverAndResumeProposalJSON(consumerID, subject, substitute string) string {
	authority := s.queryGovAuthority()
	return fmt.Sprintf(`{
  "messages": [{
    "@type": "/ibc.core.client.v1.MsgRecoverClient",
    "subject_client_id": %q,
    "substitute_client_id": %q,
    "signer": %q
  }, {
    "@type": "/vaas.provider.v1.MsgResumeConsumer",
    "authority": %q,
    "consumer_id": %q
  }],
  "metadata": "ipfs://test",
  "deposit": "10000000%s",
  "title": "Recover the forked consumer's client and resume it",
  "summary": "fork drill"
}`, subject, substitute, authority, authority, consumerID, bondDenom)
}

// resumableAtFromFailure reads, from a failed resume's reason, the time the
// provider said the resume is allowed at ("resume at or after <RFC3339>").
func resumableAtFromFailure(failure string) time.Time {
	m := regexp.MustCompile(`resume at or after (\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`).FindStringSubmatch(failure)
	if len(m) != 2 {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, m[1])
	if err != nil {
		return time.Time{}
	}
	return t
}

// clientLatestHeight returns the latest height of a provider-side IBC
// client's state.
func (s *ByzantineConsumerIntegrationTestSuite) clientLatestHeight(clientID string) int64 {
	height, err := s.tryClientLatestHeight(clientID)
	s.Require().NoErrorf(err, "failed to read the latest height of client %s", clientID)
	return height
}

// tryClientLatestHeight is clientLatestHeight returning an error instead of
// failing the test, for the conditions of Eventually and Never, which run in
// a goroutine of their own: a failure there stalls the wait to its timeout.
func (s *ByzantineConsumerIntegrationTestSuite) tryClientLatestHeight(clientID string) (int64, error) {
	stdout, stderr, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "client", "state", clientID, "--home", providerHomePath, "-o", "json",
	})
	if err != nil {
		return 0, fmt.Errorf("querying client %s: %w (stderr=%s)", clientID, err, stderr.String())
	}
	var res struct {
		ClientState struct {
			LatestHeight struct {
				RevisionHeight string `json:"revision_height"`
			} `json:"latest_height"`
		} `json:"client_state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return 0, fmt.Errorf("decoding client %s: %w (raw=%s)", clientID, err, stdout.String())
	}
	height, err := strconv.ParseInt(res.ClientState.LatestHeight.RevisionHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("client %s latest height: %q: %w", clientID, res.ClientState.LatestHeight.RevisionHeight, err)
	}
	return height, nil
}
