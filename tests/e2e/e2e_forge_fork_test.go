package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	stdout, _, err := s.dockerExec(s.providerValRes[0].Container.ID, []string{
		providerBinary, "query", "ibc", "client", "status", clientID,
		"--home", providerHomePath,
		"--output", "json",
	})
	s.Require().NoErrorf(err, "failed to query the status of client %s", clientID)
	var res struct {
		Status string `json:"status"`
	}
	s.Require().NoErrorf(json.Unmarshal(stdout.Bytes(), &res), "failed to decode the client status: %s", stdout.String())
	return res.Status
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

		type state struct {
			jailed bool
			tokens string
		}
		before := map[int]state{}
		for i := range s.validatorCount() {
			jailed, tokens := s.stakingValidatorState(s.validator(i).valoper)
			before[i] = state{jailed, tokens}
		}

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

		s.Require().Equalf("Frozen", s.clientStatus(clientID), "the fork must freeze client %s", clientID)
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "the fork must pause the consumer")
		for i, was := range before {
			jailed, tokens := s.stakingValidatorState(s.validator(i).valoper)
			s.Require().Equalf(was.jailed, jailed, "the fork changed val%d's jail", i)
			s.Require().Equalf(was.tokens, tokens, "the fork changed val%d's stake", i)
		}

		proposalID := s.submitProposalWithVote(s.resumeProposalJSON(consumerID, "Resume consumer (forked)"), "yes", "PROPOSAL_STATUS_FAILED")
		s.Require().Truef(strings.Contains(s.dumpProposal(proposalID), "consumer client is not active"),
			"the resume must fail because the client is frozen: %s", s.dumpProposal(proposalID))
		s.Require().Equalf(phasePaused, s.queryProviderConsumerPhase(consumerID), "a refused resume must leave the consumer paused")
	})
}
