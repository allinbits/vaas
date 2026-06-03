package keeper_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	tmproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	tmtypes "github.com/cometbft/cometbft/types"
	cometversion "github.com/cometbft/cometbft/version"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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
	require.Equal(t, uint64(0), response.ConsumerId)
	actualMetadata, err := providerKeeper.GetConsumerMetadata(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, consumerMetadata, actualMetadata)
	ownerAddress, err := providerKeeper.GetConsumerOwnerAddress(ctx, 0)
	require.NoError(t, err)
	require.Equal(t, "submitter", ownerAddress)
	phase := providerKeeper.GetConsumerPhase(ctx, 0)
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
	require.Equal(t, uint64(1), response.ConsumerId)
	actualMetadata, err = providerKeeper.GetConsumerMetadata(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, consumerMetadata, actualMetadata)
	ownerAddress, err = providerKeeper.GetConsumerOwnerAddress(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "submitter2", ownerAddress)
	phase = providerKeeper.GetConsumerPhase(ctx, 1)
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
	require.Equal(t, uint64(0), response.ConsumerId)

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
			Owner: "owner", ConsumerId: 0, NewOwnerAddress: "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la",
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

func TestSubmitConsumerDoubleVotingRejectsMismatchedChainID(t *testing.T) {
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerID := uint64(0)
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

func TestSubmitConsumerDoubleVotingHappyPath(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerID := uint64(0)
	chainID := "consumer-chain-id"
	providerKeeper.SetConsumerChainId(ctx, consumerID, chainID)
	providerKeeper.SetConsumerPhase(ctx, consumerID, providertypes.CONSUMER_PHASE_LAUNCHED)
	providerKeeper.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	height := int64(12)

	// Build the evidence and the matching infraction header using the same signer.
	signer := tmtypes.NewMockPV()
	tmValidator := tmtypes.NewValidator(signer.PrivKey.PubKey(), 1)
	valSet := tmtypes.NewValidatorSet([]*tmtypes.Validator{tmValidator})

	blockID1 := cryptoutil.MakeBlockID([]byte("blockhash1"), 1000, []byte("partshash1"))
	blockID2 := cryptoutil.MakeBlockID([]byte("blockhash2"), 1000, []byte("partshash2"))
	now := time.Now().UTC()
	voteA := cryptoutil.MakeAndSignVote(blockID1, height, now, valSet, signer, chainID)
	voteB := cryptoutil.MakeAndSignVote(blockID2, height, now, valSet, signer, chainID)
	evidence := &tmproto.DuplicateVoteEvidence{
		VoteA:            voteA.ToProto(),
		VoteB:            voteB.ToProto(),
		TotalVotingPower: tmValidator.VotingPower,
		ValidatorPower:   tmValidator.VotingPower,
		Timestamp:        now,
	}

	header := makeHeader(t, chainID, height, valSet)

	// Staking validator matching the signer's consensus key, so consAddr matches
	// the evidence's ValidatorAddress (identity key assignment).
	pubKey, err := cryptocodec.FromCmtPubKeyInterface(signer.PrivKey.PubKey())
	require.NoError(t, err)
	stakingValidator, err := stakingtypes.NewValidator(
		sdk.ValAddress(pubKey.Address()).String(),
		pubKey,
		stakingtypes.NewDescription("", "", "", "", ""),
	)
	require.NoError(t, err)
	stakingValidator.Status = stakingtypes.Bonded

	consAddr, err := stakingValidator.GetConsAddr()
	require.NoError(t, err)
	valOperBytes, err := providerKeeper.ValidatorAddressCodec().StringToBytes(stakingValidator.GetOperator())
	require.NoError(t, err)

	// SlashValidator path (uses default infraction params: double-sign slash fraction = 0.05).
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(stakingValidator, nil).Times(1)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false).Times(1)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valOperBytes).Return([]stakingtypes.UnbondingDelegation{}, nil).Times(1)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valOperBytes).Return([]stakingtypes.Redelegation{}, nil).Times(1)
	mocks.MockStakingKeeper.EXPECT().GetLastValidatorPower(ctx, valOperBytes).Return(int64(1000), nil).Times(1)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(ctx).Return(math.NewInt(1)).Times(1)
	mocks.MockStakingKeeper.EXPECT().
		SlashWithInfractionReason(ctx, consAddr, int64(0), int64(1000), math.LegacyNewDecWithPrec(5, 2), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN).
		Return(math.NewInt(1000), nil).Times(1)

	// JailAndTombstoneValidator path (second consAddr lookup + tombstone).
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(stakingValidator, nil).Times(1)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false).Times(1)
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, consAddr).Return(nil).Times(1)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, gomock.Any()).Return(nil).Times(1)
	mocks.MockSlashingKeeper.EXPECT().Tombstone(ctx, consAddr).Return(nil).Times(1)

	msg := &providertypes.MsgSubmitConsumerDoubleVoting{
		ConsumerId:            consumerID,
		Submitter:             validSubmitter(),
		DuplicateVoteEvidence: evidence,
		InfractionBlockHeader: header,
	}

	resp, err := msgServer.SubmitConsumerDoubleVoting(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Sanity-check the chain event surfaced the expected consumer/chain attrs.
	var found bool
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != "submit_consumer_double_voting" {
			continue
		}
		found = true
		var sawConsumerId, sawChainId bool
		for _, attr := range ev.Attributes {
			if attr.Key == "consumer_id" && attr.Value == "0" {
				sawConsumerId = true
			}
			if attr.Key == "consumer_chain_id" && attr.Value == chainID {
				sawChainId = true
			}
		}
		require.True(t, sawConsumerId, "consumer_id attribute missing on event")
		require.True(t, sawChainId, "consumer_chain_id attribute missing on event")
	}
	require.True(t, found, "submit_consumer_double_voting event not emitted")
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

func TestSetConsumerFeesPerBlock(t *testing.T) {
	// Each case starts with a fresh keeper. By default a target consumer in
	// REGISTERED phase is created; setting skipConsumer=true skips that and
	// uses a bogus id (used for the not-found case). A non-nil seedOverride
	// is set on the target consumer before the handler is invoked. An empty
	// overrideAuth resolves to k.GetAuthority() (the canonical gov authority).
	// A nil wantAmount means the override entry should be absent after the
	// call; a non-nil wantAmount asserts the stored value.
	// The module-wide default acts as a floor; overrides must exceed it.
	const globalFeesPerBlock = providertypes.DefaultFeesPerBlockAmount // 1000

	cases := []struct {
		name         string
		overrideAuth string
		skipConsumer bool
		deleted      bool
		seedOverride math.Int
		amount       string
		wantErr      error
		wantErrMsg   string
		wantAmount   math.Int
	}{
		{
			name:         "non-gov authority rejected",
			overrideAuth: "atone1notthegovauth000000000000000000000000",
			amount:       "2500",
			wantErrMsg:   "invalid authority",
		},
		{
			name:         "nonexistent consumer rejected",
			skipConsumer: true,
			amount:       "2500",
			wantErr:      providertypes.ErrUnknownConsumerId,
		},
		{
			name:    "deleted consumer rejected",
			deleted: true,
			amount:  "2500",
			wantErr: providertypes.ErrUnknownConsumerId,
		},
		{
			name:       "amount above global stored",
			amount:     "2500",
			wantAmount: math.NewInt(2500),
		},
		{
			name:       "amount equal to global rejected",
			amount:     strconv.FormatInt(globalFeesPerBlock, 10),
			wantErrMsg: "must be greater than",
		},
		{
			name:       "amount below global rejected",
			amount:     "500",
			wantErrMsg: "must be greater than",
		},
		{
			name:       "zero amount rejected",
			amount:     "0",
			wantErrMsg: "must be greater than",
		},
		{
			name:         "empty amount clears existing override",
			seedOverride: math.NewInt(2500),
			amount:       "",
		},
		{
			name:   "empty amount when nothing to clear is a no-op",
			amount: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()
			k.SetParams(ctx, providertypes.DefaultParams())

			var consumerId uint64
			switch {
			case tc.skipConsumer:
				consumerId = 999
			case tc.deleted:
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_DELETED)
			default:
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
			}

			if !tc.seedOverride.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, tc.seedOverride))
			}

			authority := tc.overrideAuth
			if authority == "" {
				authority = k.GetAuthority()
			}

			msgSrv := providerkeeper.NewMsgServerImpl(&k)
			_, err := msgSrv.SetConsumerFeesPerBlock(ctx, &providertypes.MsgSetConsumerFeesPerBlock{
				Authority:  authority,
				ConsumerId: consumerId,
				Amount:     tc.amount,
			})

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			if tc.wantErrMsg != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrMsg)
				return
			}
			require.NoError(t, err)

			if tc.wantAmount.IsNil() {
				has, err := k.ConsumerFeesPerBlockOverride.Has(ctx, consumerId)
				require.NoError(t, err)
				require.False(t, has)
			} else {
				stored, err := k.ConsumerFeesPerBlockOverride.Get(ctx, consumerId)
				require.NoError(t, err)
				require.Equal(t, tc.wantAmount, stored)
			}
		})
	}
}

// TestUpdateParams_ReconcilesFeesPerBlockOverrides verifies that raising the
// global fees_per_block drops any per-consumer override that is no longer
// strictly greater than the new default, while keeping those still above it.
func TestUpdateParams_ReconcilesFeesPerBlockOverrides(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()
	k.SetParams(ctx, providertypes.DefaultParams()) // global default = 1000

	// Three overrides, all above the current floor (1000).
	below := k.FetchAndIncrementConsumerId(ctx) // 1500: below the new floor -> dropped
	equal := k.FetchAndIncrementConsumerId(ctx) // 2000: equal the new floor -> dropped
	above := k.FetchAndIncrementConsumerId(ctx) // 2500: above the new floor -> kept
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, below, math.NewInt(1500)))
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, equal, math.NewInt(2000)))
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, above, math.NewInt(2500)))

	// Raise the global default to 2000.
	newParams := providertypes.DefaultParams()
	newParams.FeesPerBlock = sdk.NewInt64Coin(providertypes.DefaultFeesPerBlockDenom, 2000)

	msgSrv := providerkeeper.NewMsgServerImpl(&k)
	_, err := msgSrv.UpdateParams(ctx, &providertypes.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    newParams,
	})
	require.NoError(t, err)

	// Overrides not strictly greater than the new floor are dropped.
	for _, id := range []uint64{below, equal} {
		has, err := k.ConsumerFeesPerBlockOverride.Has(ctx, id)
		require.NoError(t, err)
		require.False(t, has, "override for consumer %d should have been dropped", id)
	}

	// Overrides still above the new floor survive untouched.
	amtAbove, err := k.ConsumerFeesPerBlockOverride.Get(ctx, above)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(2500), amtAbove)

	// Lowering the floor never invalidates overrides: the survivor stays.
	lowerParams := providertypes.DefaultParams()
	lowerParams.FeesPerBlock = sdk.NewInt64Coin(providertypes.DefaultFeesPerBlockDenom, 500)
	_, err = msgSrv.UpdateParams(ctx, &providertypes.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    lowerParams,
	})
	require.NoError(t, err)
	amtAbove, err = k.ConsumerFeesPerBlockOverride.Get(ctx, above)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(2500), amtAbove)
}
