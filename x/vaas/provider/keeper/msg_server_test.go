package keeper_test

import (
	"sync"
	"testing"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	cometversion "github.com/cometbft/cometbft/version"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

var keeperBech32CfgOnce sync.Once

func setupKeeperBech32Cfg() {
	keeperBech32CfgOnce.Do(func() {
		cfg := sdk.GetConfig()
		cfg.SetBech32PrefixForAccount("cosmos", "cosmospub")
		cfg.SetBech32PrefixForValidator("cosmosvaloper", "cosmosvaloperpub")
		cfg.SetBech32PrefixForConsensusNode("cosmosvalcons", "cosmosvalconspub")
		cfg.Seal()
	})
}

func validSubmitter() string {
	setupKeeperBech32Cfg()
	return sdk.AccAddress(make([]byte, 20)).String()
}

func TestCreateConsumer(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerMetadata := providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description",
	}
	response, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{},
		})
	require.NoError(t, err)
	require.Equal(t, "0", response.ConsumerId)
	actualMetadata, err := providerKeeper.GetConsumerMetadata(ctx, "0")
	require.NoError(t, err)
	require.Equal(t, consumerMetadata, actualMetadata)
	ownerAddress, err := providerKeeper.GetConsumerOwnerAddress(ctx, "0")
	require.NoError(t, err)
	require.Equal(t, "submitter", ownerAddress)
	phase := providerKeeper.GetConsumerPhase(ctx, "0")
	require.Equal(t, providertypes.CONSUMER_PHASE_REGISTERED, phase)

	// Create another consumer with a different chain id
	consumerMetadata = providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description2",
	}
	response, err = msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter2", ChainId: "chainId2", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{},
		})
	require.NoError(t, err)
	// assert that the consumer id is different from the previously registered chain
	require.Equal(t, "1", response.ConsumerId)
	actualMetadata, err = providerKeeper.GetConsumerMetadata(ctx, "1")
	require.NoError(t, err)
	require.Equal(t, consumerMetadata, actualMetadata)
	ownerAddress, err = providerKeeper.GetConsumerOwnerAddress(ctx, "1")
	require.NoError(t, err)
	require.Equal(t, "submitter2", ownerAddress)
	phase = providerKeeper.GetConsumerPhase(ctx, "1")
	require.Equal(t, providertypes.CONSUMER_PHASE_REGISTERED, phase)
}

func TestCreateConsumerDuplicateChainId(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerMetadata := providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description",
	}

	// Register a consumer with chainId "duplicateChainId"
	response, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter1", ChainId: "duplicateChainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{},
		})
	require.NoError(t, err)
	require.Equal(t, "0", response.ConsumerId)

	// Attempt to register another consumer with the same chainId
	_, err = msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter2", ChainId: "duplicateChainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{},
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrDuplicateChainId)
}

func TestUpdateConsumer(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// try to update a non-existing consumer
	_, err := msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner: "owner", ConsumerId: "0", NewOwnerAddress: "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la",
		})
	require.Error(t, err, "cannot update consumer chain")

	// create a chain before updating it
	chainId := "chainId-1"
	createConsumerResponse, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: chainId,
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
				Metadata:    "metadata",
			},
		})
	require.NoError(t, err)
	consumerId := createConsumerResponse.ConsumerId

	mocks.MockAccountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner: "wrong owner", ConsumerId: consumerId, NewOwnerAddress: "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la",
		})
	require.Error(t, err, "expected owner address")

	// assert that we can change the chain id of a registered chain
	expectedChainId := "newChainId-1"
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner: "submitter", ConsumerId: consumerId,
			NewChainId: expectedChainId,
		})
	require.NoError(t, err)
	actualChainId, err := providerKeeper.GetConsumerChainId(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expectedChainId, actualChainId)

	// assert that we can update metadata
	expectedConsumerMetadata := providertypes.ConsumerMetadata{
		Name:        "name2",
		Description: "description2",
		Metadata:    "metadata2",
	}

	expectedOwnerAddress := "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner: "submitter", ConsumerId: consumerId, NewOwnerAddress: expectedOwnerAddress,
			Metadata: &expectedConsumerMetadata,
		})
	require.NoError(t, err)

	// assert that owner address was updated
	ownerAddress, err := providerKeeper.GetConsumerOwnerAddress(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expectedOwnerAddress, ownerAddress)

	// assert that consumer metadata were updated
	actualConsumerMetadata, err := providerKeeper.GetConsumerMetadata(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expectedConsumerMetadata, actualConsumerMetadata)
}

func TestUpdateConsumerDuplicateChainId(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// create a chain that register chainId-1
	chainId1 := "chainId-1"
	createConsumerResponse, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: chainId1,
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
				Metadata:    "metadata",
			},
		})
	require.NoError(t, err)

	// create a chain that register chainId-2
	chainId2 := "chainId2-1"
	createConsumerResponse, err = msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: chainId2,
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
				Metadata:    "metadata",
			},
		})
	require.NoError(t, err)
	consumerId2 := createConsumerResponse.ConsumerId

	// assert that comsumerId2 cannot use a registered chain id
	expectedChainId := "chainId-1"
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner: "submitter", ConsumerId: consumerId2,
			NewChainId: expectedChainId,
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrDuplicateChainId)
	actualChainId, err := providerKeeper.GetConsumerChainId(ctx, consumerId2)
	require.NoError(t, err)
	require.Equal(t, chainId2, actualChainId)
}

func TestSubmitConsumerMisbehaviourRejectsNilMessage(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerMisbehaviour(ctx, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "message cannot be nil")
	})
}

func TestSubmitConsumerMisbehaviourRejectsNilSignedHeader(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	msg := &providertypes.MsgSubmitConsumerMisbehaviour{
		ConsumerId: "0",
		Submitter:  validSubmitter(),
		Misbehaviour: &ibctmtypes.Misbehaviour{
			Header1: &ibctmtypes.Header{},
			Header2: &ibctmtypes.Header{},
		},
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerMisbehaviour(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Misbehaviour")
	})
}

func TestSubmitConsumerDoubleVotingRejectsNilHeaderSignedHeader(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerID := "0"
	chainID := "consumer-chain-id"
	providerKeeper.SetConsumerChainId(ctx, consumerID, chainID)

	evidence := makeDuplicateVoteEvidenceProto(t, chainID, 12)
	header := &ibctmtypes.Header{} // SignedHeader is nil

	msg := &providertypes.MsgSubmitConsumerDoubleVoting{
		ConsumerId:            consumerID,
		Submitter:             validSubmitter(),
		DuplicateVoteEvidence: evidence,
		InfractionBlockHeader: header,
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerDoubleVoting(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "ValidateTendermintHeader")
	})
}

func TestSubmitConsumerDoubleVotingRejectsMismatchedChainID(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerID := "0"
	storedChainID := "consumer-chain-id"
	differentChainID := "different-chain-id"
	providerKeeper.SetConsumerChainId(ctx, consumerID, storedChainID)

	height := int64(12)
	evidence, valSet := makeDuplicateVoteEvidenceProtoWithValSet(t, differentChainID, height)
	header := makeHeader(t, differentChainID, height, valSet)

	msg := &providertypes.MsgSubmitConsumerDoubleVoting{
		ConsumerId:            consumerID,
		Submitter:             validSubmitter(),
		DuplicateVoteEvidence: evidence,
		InfractionBlockHeader: header,
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerDoubleVoting(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "infraction block header chain id")
		require.Contains(t, err.Error(), "does not match consumer chain id")
	})
}

func TestSubmitConsumerDoubleVotingRejectsMismatchedHeights(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerID := "0"
	chainID := "consumer-chain-id"
	providerKeeper.SetConsumerChainId(ctx, consumerID, chainID)

	evidenceHeight := int64(12)
	headerHeight := int64(15)
	evidence, valSet := makeDuplicateVoteEvidenceProtoWithValSet(t, chainID, evidenceHeight)
	header := makeHeader(t, chainID, headerHeight, valSet)

	msg := &providertypes.MsgSubmitConsumerDoubleVoting{
		ConsumerId:            consumerID,
		Submitter:             validSubmitter(),
		DuplicateVoteEvidence: evidence,
		InfractionBlockHeader: header,
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerDoubleVoting(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "infraction block header height")
		require.Contains(t, err.Error(), "does not match duplicate vote evidence height")
	})
}

func makeHeader(t *testing.T, chainID string, height int64, valSet *tmtypes.ValidatorSet) *ibctmtypes.Header {
	t.Helper()

	blockTime := time.Now().UTC()
	header := tmtypes.Header{
		Version: cmtversion.Consensus{Block: cometversion.BlockProtocol},
		ChainID: chainID,
		Height:  height,
		Time:    blockTime,

		ValidatorsHash:     valSet.Hash(),
		NextValidatorsHash: valSet.Hash(),
		ProposerAddress:    valSet.Proposer.Address,
	}

	commit := &tmtypes.Commit{
		Height:  height,
		Round:   0,
		BlockID: tmtypes.BlockID{Hash: header.Hash()},
		Signatures: []tmtypes.CommitSig{
			{
				BlockIDFlag:      tmtypes.BlockIDFlagCommit,
				ValidatorAddress: valSet.Proposer.Address,
				Timestamp:        blockTime,
				Signature:        []byte{0x01},
			},
		},
	}

	tmSignedHeader := tmtypes.SignedHeader{
		Header: &header,
		Commit: commit,
	}

	protoValSet, err := valSet.ToProto()
	require.NoError(t, err)

	revision := clienttypes.ParseChainID(chainID)

	return &ibctmtypes.Header{
		SignedHeader:      tmSignedHeader.ToProto(),
		ValidatorSet:      protoValSet,
		TrustedHeight:     clienttypes.NewHeight(revision, uint64(height-1)),
		TrustedValidators: protoValSet,
	}
}

func makeDuplicateVoteEvidenceProto(t *testing.T, chainID string, height int64) *tmproto.DuplicateVoteEvidence {
	t.Helper()

	evidence, _ := makeDuplicateVoteEvidenceProtoWithValSet(t, chainID, height)
	return evidence
}

func makeDuplicateVoteEvidenceProtoWithValSet(t *testing.T, chainID string, height int64) (*tmproto.DuplicateVoteEvidence, *tmtypes.ValidatorSet) {
	t.Helper()

	signer := tmtypes.NewMockPV()
	validator := tmtypes.NewValidator(signer.PrivKey.PubKey(), 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{validator})

	blockID1 := cryptoutil.MakeBlockID([]byte("blockhash1"), 1000, []byte("partshash1"))
	blockID2 := cryptoutil.MakeBlockID([]byte("blockhash2"), 1000, []byte("partshash2"))
	now := time.Now().UTC()

	voteA := cryptoutil.MakeAndSignVote(blockID1, height, now, valSet, signer, chainID)
	voteB := cryptoutil.MakeAndSignVote(blockID2, height, now, valSet, signer, chainID)

	return &tmproto.DuplicateVoteEvidence{
		VoteA:            voteA.ToProto(),
		VoteB:            voteB.ToProto(),
		TotalVotingPower: validator.VotingPower,
		ValidatorPower:   validator.VotingPower,
		Timestamp:        now,
	}, valSet
}
