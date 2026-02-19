package keeper_test

import (
	"testing"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	tmtypes "github.com/cometbft/cometbft/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"

	"github.com/cosmos/cosmos-sdk/codec/address"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

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

	// Create another consumer
	consumerMetadata = providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description2",
	}
	response, err = msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter2", ChainId: "chainId", Metadata: consumerMetadata,
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
		Submitter:  "submitter",
		Misbehaviour: &ibctmtypes.Misbehaviour{
			Header1: &ibctmtypes.Header{},
			Header2: &ibctmtypes.Header{},
		},
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerMisbehaviour(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "misbehaviour cannot be nil")
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
		Submitter:             "submitter",
		DuplicateVoteEvidence: evidence,
		InfractionBlockHeader: header,
	}

	require.NotPanics(t, func() {
		_, err := msgServer.SubmitConsumerDoubleVoting(ctx, msg)
		require.Error(t, err)
		require.Contains(t, err.Error(), "infraction block header cannot be nil")
	})
}

func makeDuplicateVoteEvidenceProto(t *testing.T, chainID string, height int64) *tmproto.DuplicateVoteEvidence {
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
	}
}
