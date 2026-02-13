package types_test

import (
	"sync"
	"testing"
	"time"

	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"
	cometversion "github.com/cometbft/cometbft/version"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

var bech32CfgOnce sync.Once

func setupBech32Cfg() {
	bech32CfgOnce.Do(func() {
		cfg := sdk.GetConfig()
		cfg.SetBech32PrefixForAccount("cosmos", "cosmospub")
		cfg.SetBech32PrefixForValidator("cosmosvaloper", "cosmosvaloperpub")
		cfg.SetBech32PrefixForConsensusNode("cosmosvalcons", "cosmosvalconspub")
		cfg.Seal()
	})
}

func makeValidIBCTMHeader(t *testing.T, chainID string, height int64, valSet *tmtypes.ValidatorSet, blockTime time.Time) *ibctmtypes.Header {
	t.Helper()

	h := tmtypes.Header{
		Version: cmtversion.Consensus{Block: cometversion.BlockProtocol},
		ChainID: chainID,
		Height:  height,
		Time:    blockTime,

		ValidatorsHash:     valSet.Hash(),
		NextValidatorsHash: valSet.Hash(),

		// Optional fields left empty/zero for basic validation.
		ProposerAddress: valSet.Proposer.Address,
	}

	commit := &tmtypes.Commit{
		Height:  height,
		Round:   0,
		BlockID: tmtypes.BlockID{Hash: h.Hash()},
		Signatures: []tmtypes.CommitSig{
			{
				BlockIDFlag:      tmtypes.BlockIDFlagCommit,
				ValidatorAddress: valSet.Proposer.Address,
				Timestamp:        blockTime,
				Signature:        []byte{0x01},
			},
		},
	}

	signedHeader := &tmtypes.SignedHeader{
		Header: &h,
		Commit: commit,
	}

	valSetProto, err := valSet.ToProto()
	require.NoError(t, err)

	revision := clienttypes.ParseChainID(chainID)
	return &ibctmtypes.Header{
		SignedHeader:      signedHeader.ToProto(),
		ValidatorSet:      valSetProto,
		TrustedHeight:     clienttypes.NewHeight(revision, uint64(height-1)),
		TrustedValidators: valSetProto,
	}
}

func makeValidDoubleVoteEvidenceAndHeader(t *testing.T, chainID string, height int64) (*tmproto.DuplicateVoteEvidence, *ibctmtypes.Header) {
	t.Helper()

	blockTime := time.Now().UTC()

	signer := tmtypes.NewMockPV()
	val := tmtypes.NewValidator(signer.PrivKey.PubKey(), 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{val})

	blockID1 := cryptoutil.MakeBlockID([]byte("blockhash"), 1000, []byte("partshash"))
	blockID2 := cryptoutil.MakeBlockID([]byte("blockhash2"), 1000, []byte("partshash"))

	vote1 := cryptoutil.MakeAndSignVote(blockID1, height, blockTime, valSet, signer, chainID)
	vote2 := cryptoutil.MakeAndSignVote(blockID2, height, blockTime, valSet, signer, chainID)

	dve, err := tmtypes.NewDuplicateVoteEvidence(vote1, vote2, blockTime, valSet)
	require.NoError(t, err)

	header := makeValidIBCTMHeader(t, chainID, height, valSet, blockTime)
	return dve.ToProto(), header
}

func TestMsgSubmitConsumerMisbehaviourValidateBasic_NilMisbehaviourDoesNotPanic(t *testing.T) {
	setupBech32Cfg()

	submitter := sdk.AccAddress(make([]byte, 20)).String()
	msg := types.MsgSubmitConsumerMisbehaviour{
		Submitter:    submitter,
		Misbehaviour: nil,
		ConsumerId:   "1",
	}

	require.NotPanics(t, func() {
		err := msg.ValidateBasic()
		require.Error(t, err)
	})
}

func TestMsgSubmitConsumerDoubleVotingValidateBasic_RejectsInvalidSubmitter(t *testing.T) {
	setupBech32Cfg()

	ev, header := makeValidDoubleVoteEvidenceAndHeader(t, "consumer-1", 10)

	msg := types.MsgSubmitConsumerDoubleVoting{
		Submitter:             "not-a-bech32-address",
		DuplicateVoteEvidence: ev,
		InfractionBlockHeader: header,
		ConsumerId:            "1",
	}

	err := msg.ValidateBasic()
	require.Error(t, err)
}

func TestMsgSubmitConsumerDoubleVotingValidateBasic_RejectsHeightMismatch(t *testing.T) {
	setupBech32Cfg()

	submitter := sdk.AccAddress(make([]byte, 20)).String()
	ev, _ := makeValidDoubleVoteEvidenceAndHeader(t, "consumer-1", 10)
	_, headerWrongHeight := makeValidDoubleVoteEvidenceAndHeader(t, "consumer-1", 11)

	msg := types.MsgSubmitConsumerDoubleVoting{
		Submitter:             submitter,
		DuplicateVoteEvidence: ev,
		InfractionBlockHeader: headerWrongHeight,
		ConsumerId:            "1",
	}

	err := msg.ValidateBasic()
	require.Error(t, err)
}

func TestMsgSubmitConsumerDoubleVotingValidateBasic_AcceptsValidMsg(t *testing.T) {
	setupBech32Cfg()

	submitter := sdk.AccAddress(make([]byte, 20)).String()
	ev, header := makeValidDoubleVoteEvidenceAndHeader(t, "consumer-1", 10)

	msg := types.MsgSubmitConsumerDoubleVoting{
		Submitter:             submitter,
		DuplicateVoteEvidence: ev,
		InfractionBlockHeader: header,
		ConsumerId:            "1",
	}

	require.NoError(t, msg.ValidateBasic())
}

