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

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	cryptoutil "github.com/allinbits/vaas/testutil/crypto"
	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerMetadata := providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description",
	}
	response, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
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
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
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
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	// only the first CreateConsumer reaches validateConsumerUnbonding; the duplicate is rejected earlier
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	consumerMetadata := providertypes.ConsumerMetadata{
		Name:        "chain name",
		Description: "description",
	}

	// Register a consumer with chainId "duplicateChainId"
	response, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter1", ChainId: "duplicateChainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	require.Equal(t, uint64(0), response.ConsumerId)

	// Attempt to register another consumer with the same chainId — rejected before unbonding check
	_, err = msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter2", ChainId: "duplicateChainId", Metadata: consumerMetadata,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrDuplicateChainId)
}

func TestUpdateConsumer(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

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
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(2)

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
	newParams.FeesPerBlockAmount = math.NewInt(2000)

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
	lowerParams.FeesPerBlockAmount = math.NewInt(500)
	_, err = msgSrv.UpdateParams(ctx, &providertypes.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    lowerParams,
	})
	require.NoError(t, err)
	amtAbove, err = k.ConsumerFeesPerBlockOverride.Get(ctx, above)
	require.NoError(t, err)
	require.Equal(t, math.NewInt(2500), amtAbove)
}

func TestRemoveConsumerGovAuth(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// CreateConsumer calls validateConsumerUnbonding; RemoveConsumer also calls UnbondingTime
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(2)

	// create a consumer chain and set it to LAUNCHED
	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	// non-authority should be rejected
	_, err = msgServer.RemoveConsumer(ctx,
		&providertypes.MsgRemoveConsumer{
			Authority:  "cosmos1notthegovauth000000000000000000000000",
			ConsumerId: consumerId,
		})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid authority")

	// correct authority succeeds
	_, err = msgServer.RemoveConsumer(ctx,
		&providertypes.MsgRemoveConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: consumerId,
		})
	require.NoError(t, err)

	// verify the chain is stopped
	phase := providerKeeper.GetConsumerPhase(ctx, consumerId)
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, phase)
}

func TestRemoveConsumerNonLaunchedRejected(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// create a consumer chain (stays in REGISTERED phase)
	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)

	// gov authority cannot remove a non-launched chain
	_, err = msgServer.RemoveConsumer(ctx,
		&providertypes.MsgRemoveConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: createResp.ConsumerId,
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
}

// TestRemoveConsumerFromPaused verifies that MsgRemoveConsumer accepts a
// PAUSED consumer (not just LAUNCHED), routes into
// StopAndPrepareForConsumerRemoval, and clears the pause auto-stop schedule
// so a stale entry does not later fire against the now-STOPPED consumer.
func TestRemoveConsumerFromPaused(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(2)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, providerKeeper.PauseConsumerChain(ctx, consumerId))
	expirationTime, err := providerKeeper.GetConsumerPauseExpirationTime(ctx, consumerId)
	require.NoError(t, err)

	_, err = msgServer.RemoveConsumer(ctx,
		&providertypes.MsgRemoveConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: consumerId,
		})
	require.NoError(t, err)

	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, providerKeeper.GetConsumerPhase(ctx, consumerId))

	_, err = providerKeeper.GetConsumerPauseExpirationTime(ctx, consumerId)
	require.Error(t, err)
	queued, err := providerKeeper.GetConsumersToBeAutoStopped(ctx, expirationTime)
	require.NoError(t, err)
	require.Empty(t, queued.Ids)
}

// TestResumeConsumerGovAuth verifies MsgResumeConsumer's authority gate and,
// on success, that it delegates to ResumeConsumerChain: the paused consumer
// flips back to LAUNCHED with an active client.
func TestResumeConsumerGovAuth(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	providerKeeper.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, providerKeeper.PauseConsumerChain(ctx, consumerId))

	// non-authority is rejected
	_, err = msgServer.ResumeConsumer(ctx,
		&providertypes.MsgResumeConsumer{
			Authority:  "cosmos1notthegovauth000000000000000000000000",
			ConsumerId: consumerId,
		})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid authority")
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, providerKeeper.GetConsumerPhase(ctx, consumerId))

	// correct authority succeeds
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active)
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)

	_, err = msgServer.ResumeConsumer(ctx,
		&providertypes.MsgResumeConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: consumerId,
		})
	require.NoError(t, err)

	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, providerKeeper.GetConsumerPhase(ctx, consumerId))
}

// TestResumeConsumerNonPausedRejected verifies MsgResumeConsumer rejects a
// consumer that is not in the PAUSED phase.
func TestResumeConsumerNonPausedRejected(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	providerKeeper.SetConsumerPhase(ctx, createResp.ConsumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	_, err = msgServer.ResumeConsumer(ctx,
		&providertypes.MsgResumeConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: createResp.ConsumerId,
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
}

// TestResumeConsumerRejectsInactiveClient verifies MsgResumeConsumer's
// client pre-flight rejects an expired/frozen client with guidance to bundle
// ibc-go's MsgRecoverClient into the same governance proposal (see
// docs/consumer-downtime.md, "The PAUSED phase").
func TestResumeConsumerRejectsInactiveClient(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	providerKeeper.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, providerKeeper.PauseConsumerChain(ctx, consumerId))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Expired)

	_, err = msgServer.ResumeConsumer(ctx,
		&providertypes.MsgResumeConsumer{
			Authority:  providerKeeper.GetAuthority(),
			ConsumerId: consumerId,
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrConsumerClientNotActive)
	require.Contains(t, err.Error(), "MsgRecoverClient")
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, providerKeeper.GetConsumerPhase(ctx, consumerId))
}

func TestUpdateConsumerLaunchedOnlyMetadata(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// create a consumer chain
	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	// set to LAUNCHED
	providerKeeper.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)

	mocks.MockAccountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	// metadata update should succeed
	newMetadata := providertypes.ConsumerMetadata{
		Name:        "new-name",
		Description: "new-description",
		Metadata:    "new-metadata",
	}
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner:      "submitter",
			ConsumerId: consumerId,
			Metadata:   &newMetadata,
		})
	require.NoError(t, err)
	actualMetadata, err := providerKeeper.GetConsumerMetadata(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, newMetadata, actualMetadata)

	// chain-id update should fail
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner:      "submitter",
			ConsumerId: consumerId,
			NewChainId: "newChainId",
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrInvalidPhase)

	// owner transfer should succeed on launched chain
	newOwner := "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la"
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner:           "submitter",
			ConsumerId:      consumerId,
			NewOwnerAddress: newOwner,
		})
	require.NoError(t, err)
	actualOwner, err := providerKeeper.GetConsumerOwnerAddress(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, newOwner, actualOwner)

	// initialization parameters update should fail (use new owner since ownership was transferred)
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner:      newOwner,
			ConsumerId: consumerId,
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				SpawnTime: time.Now().Add(24 * time.Hour),
			},
		})
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
}

func TestUpdateConsumerPreLaunchAllowsAll(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	// create a consumer chain (REGISTERED phase)
	createResp, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter", ChainId: "chainId-1",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
		})
	require.NoError(t, err)
	consumerId := createResp.ConsumerId

	mocks.MockAccountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

	// all updates should succeed in pre-launch phase
	_, err = msgServer.UpdateConsumer(ctx,
		&providertypes.MsgUpdateConsumer{
			Owner:           "submitter",
			ConsumerId:      consumerId,
			NewChainId:      "newChainId-1",
			NewOwnerAddress: "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la",
			Metadata: &providertypes.ConsumerMetadata{
				Name:        "new-name",
				Description: "new-description",
			},
		})
	require.NoError(t, err)

	// verify chain id updated
	chainId, err := providerKeeper.GetConsumerChainId(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, "newChainId-1", chainId)

	// verify owner updated
	owner, err := providerKeeper.GetConsumerOwnerAddress(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, "cosmos1dkas8mu4kyhl5jrh4nzvm65qz588hy9qcz08la", owner)
}

func TestCreateConsumerEventsIncludeInitParams(t *testing.T) {
	providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

	binaryHash := []byte("deadbeef")
	genesisHash := []byte("cafebabe")
	spawnTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	_, err := msgServer.CreateConsumer(ctx,
		&providertypes.MsgCreateConsumer{
			Submitter: "submitter",
			ChainId:   "chainId",
			Metadata: providertypes.ConsumerMetadata{
				Name:        "name",
				Description: "description",
			},
			InitializationParameters: &providertypes.ConsumerInitializationParameters{
				BinaryHash:      binaryHash,
				GenesisHash:     genesisHash,
				SpawnTime:       spawnTime,
				UnbondingPeriod: 21 * 24 * time.Hour,
			},
		})
	require.NoError(t, err)

	// verify the event has binary_hash, genesis_hash, and spawn_time attributes
	var found bool
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != "create_consumer" {
			continue
		}
		found = true
		var sawBinaryHash, sawGenesisHash, sawSpawnTime bool
		for _, attr := range ev.Attributes {
			if attr.Key == "consumer_binary_hash" && attr.Value == string(binaryHash) {
				sawBinaryHash = true
			}
			if attr.Key == "consumer_genesis_hash" && attr.Value == string(genesisHash) {
				sawGenesisHash = true
			}
			if attr.Key == "consumer_spawn_time" {
				sawSpawnTime = true
			}
		}
		require.True(t, sawBinaryHash, "consumer_binary_hash attribute missing")
		require.True(t, sawGenesisHash, "consumer_genesis_hash attribute missing")
		require.True(t, sawSpawnTime, "consumer_spawn_time attribute missing")
	}
	require.True(t, found, "create_consumer event not emitted")
}

func TestCreateConsumer_PopulatesReverseLookup(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).Times(1)

	ms := providerkeeper.NewMsgServerImpl(&k)

	resp, err := ms.CreateConsumer(ctx, &providertypes.MsgCreateConsumer{
		Submitter: "submitter", ChainId: "chainId",
		Metadata: providertypes.ConsumerMetadata{Name: "n", Description: "d"},
		InitializationParameters: &providertypes.ConsumerInitializationParameters{
			UnbondingPeriod: 21 * 24 * time.Hour,
		},
	})
	require.NoError(t, err)

	poolAddr := k.GetConsumerFeePoolAddress(resp.ConsumerId)
	consumerId, err := k.FeePoolAddressToConsumerId.Get(ctx, poolAddr)
	require.NoError(t, err)
	require.Equal(t, resp.ConsumerId, consumerId)
}

func TestFundConsumerFeePool(t *testing.T) {
	alice := sdk.AccAddress([]byte("alice___________"))
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)

	// regularSuccessMocks sets up the 3 bank calls for a normal (non-gov) fund.
	regularSuccessMocks := func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress, coins sdk.Coins) {
		mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, coins[0].Denom).
			Return(sdk.NewInt64Coin(coins[0].Denom, 0))
		mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
			ctx, alice, providertypes.ModuleName, coins).Return(nil)
		mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
			ctx, providertypes.ModuleName, poolAddr, coins).Return(nil)
	}

	testCases := []struct {
		name             string
		phase            providertypes.ConsumerPhase
		register         bool // whether to register a consumer
		consumerId       uint64
		govSigner        bool
		feesAmount       int64 // amount for FeesPerBlock param (default 10)
		minDepositBlocks uint64
		// seedOverride is the per-consumer fees_per_block override to seed
		// before the call. IsNil() means "no override set".
		seedOverride math.Int
		amount       sdk.Coin
		setupMocks   func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress)
		wantErr      error
		postCheck    func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, consumerId uint64)
	}{
		{
			name:     "regular signer in REGISTERED mints shares",
			phase:    providertypes.CONSUMER_PHASE_REGISTERED,
			register: true,
			amount:   sdk.NewInt64Coin("uphoton", 200_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200_000))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, consumerId uint64) {
				shares, _ := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, "uphoton", alice))
				require.Equal(t, math.NewInt(200_000), shares)
			},
		},
		{
			name:     "allowed in INITIALIZED",
			phase:    providertypes.CONSUMER_PHASE_INITIALIZED,
			register: true,
			amount:   sdk.NewInt64Coin("uphoton", 200_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200_000))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
		},
		{
			name:     "allowed in LAUNCHED",
			phase:    providertypes.CONSUMER_PHASE_LAUNCHED,
			register: true,
			amount:   sdk.NewInt64Coin("uphoton", 200_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200_000))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
		},
		{
			name:     "allowed in STOPPED",
			phase:    providertypes.CONSUMER_PHASE_STOPPED,
			register: true,
			amount:   sdk.NewInt64Coin("uphoton", 200_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200_000))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
		},
		{
			name:      "gov authority mints distribution-module shares",
			phase:     providertypes.CONSUMER_PHASE_REGISTERED,
			register:  true,
			govSigner: true,
			amount:    sdk.NewInt64Coin("uphoton", 200_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 200_000))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
					Return(sdk.NewInt64Coin("uphoton", 0))
				mocks.MockDistributionKeeper.EXPECT().DistributeFromFeePool(
					ctx, coins, poolAddr).Return(nil)
			},
			postCheck: func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, consumerId uint64) {
				shares, _ := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, "uphoton", distrAddr))
				require.Equal(t, math.NewInt(200_000), shares)
			},
		},
		{
			name:       "rejects unknown consumer",
			register:   false,
			consumerId: 999,
			amount:     sdk.NewInt64Coin("uphoton", 1),
			wantErr:    providertypes.ErrUnknownConsumerId,
		},
		{
			name:     "rejects deleted",
			phase:    providertypes.CONSUMER_PHASE_DELETED,
			register: true,
			amount:   sdk.NewInt64Coin("uphoton", 1),
			wantErr:  providertypes.ErrInvalidPhase,
		},
		{
			name:     "rejects wrong denom",
			phase:    providertypes.CONSUMER_PHASE_REGISTERED,
			register: true,
			amount:   sdk.NewInt64Coin("uatone", 1),
			wantErr:  providertypes.ErrInvalidFundDenom,
		},
		{
			name:             "below floor rejected",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			feesAmount:       1000,
			minDepositBlocks: providertypes.DefaultMinDepositBlocks,
			amount:           sdk.NewInt64Coin("uphoton", 1000),
			wantErr:          providertypes.ErrDepositBelowMinimum,
		},
		{
			name:             "below floor rejected for gov too",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			govSigner:        true,
			feesAmount:       1000,
			minDepositBlocks: providertypes.DefaultMinDepositBlocks,
			amount:           sdk.NewInt64Coin("uphoton", 1000),
			wantErr:          providertypes.ErrDepositBelowMinimum,
		},
		{
			name:             "floor disabled when param is zero",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			minDepositBlocks: 0,
			amount:           sdk.NewInt64Coin("uphoton", 1),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
		},
		{
			// global floor = 1000 * 14400 = 14_400_000; per-consumer override
			// raises effective fee to 5000, so the per-consumer floor is
			// 5000 * 14400 = 72_000_000. A 50M deposit clears the global
			// floor but is below the per-consumer floor and must be rejected.
			name:             "override raises floor: deposit between global and override floor rejected",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			feesAmount:       1000,
			minDepositBlocks: providertypes.DefaultMinDepositBlocks,
			seedOverride:     math.NewInt(5000),
			amount:           sdk.NewInt64Coin("uphoton", 50_000_000),
			wantErr:          providertypes.ErrDepositBelowMinimum,
		},
		{
			// Same setup; deposit clears the override floor (>= 72M) and the
			// fund succeeds. Locks the override math down end-to-end.
			name:             "override raises floor: deposit above override floor accepted",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			feesAmount:       1000,
			minDepositBlocks: providertypes.DefaultMinDepositBlocks,
			seedOverride:     math.NewInt(5000),
			amount:           sdk.NewInt64Coin("uphoton", 100_000_000),
			setupMocks: func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress) {
				coins := sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100_000_000))
				regularSuccessMocks(mocks, ctx, poolAddr, coins)
			},
		},
		{
			// Override + gov signer: the floor check fires before any
			// signer-specific branching, so gov funds are subject to the same
			// per-consumer floor as regular depositors. 50M clears the global
			// floor but is below the override floor (72M) and must be rejected.
			name:             "override raises floor: gov rejected below override floor",
			phase:            providertypes.CONSUMER_PHASE_REGISTERED,
			register:         true,
			govSigner:        true,
			feesAmount:       1000,
			minDepositBlocks: providertypes.DefaultMinDepositBlocks,
			seedOverride:     math.NewInt(5000),
			amount:           sdk.NewInt64Coin("uphoton", 50_000_000),
			wantErr:          providertypes.ErrDepositBelowMinimum,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			k.SetParams(ctx, providertypes.DefaultParams())
			params := k.GetParams(ctx)
			feesAmount := tc.feesAmount
			if feesAmount == 0 {
				feesAmount = 10
			}
			params.FeesPerBlockAmount = math.NewInt(feesAmount)
			params.MinDepositBlocks = tc.minDepositBlocks
			k.SetParams(ctx, params)

			consumerId := tc.consumerId
			var poolAddr sdk.AccAddress
			if tc.register {
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, tc.phase)
				poolAddr = k.GetConsumerFeePoolAddress(consumerId)
			}

			if !tc.seedOverride.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, tc.seedOverride))
			}

			if tc.setupMocks != nil {
				tc.setupMocks(mocks, ctx, poolAddr)
			}

			ms := providerkeeper.NewMsgServerImpl(&k)
			signer := alice.String()
			if tc.govSigner {
				signer = k.GetAuthority()
			}
			_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
				Signer:     signer,
				ConsumerId: consumerId,
				Amount:     tc.amount,
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			if tc.postCheck != nil {
				tc.postCheck(t, k, ctx, consumerId)
			}
		})
	}
}

func TestWithdrawConsumerFeePool(t *testing.T) {
	alice := sdk.AccAddress([]byte("alice___________"))
	depositor := sdk.AccAddress([]byte("depositor_______"))
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)

	testCases := []struct {
		name       string
		register   bool
		consumerId uint64
		phase      providertypes.ConsumerPhase
		signer     sdk.AccAddress // nil = depositor; govSigner overrides to k.GetAuthority()
		govSigner  bool
		setup      func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress)
		amount     sdk.Coins
		wantErr    error
		postCheck  func(t *testing.T, resp *providertypes.MsgWithdrawConsumerFeePoolResponse)
	}{
		{
			name:       "unknown consumer",
			register:   false,
			consumerId: 999,
			amount:     sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)),
			wantErr:    providertypes.ErrUnknownConsumerId,
		},
		{
			name:     "deleted consumer",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_DELETED,
			amount:   sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)),
			wantErr:  providertypes.ErrInvalidPhase,
		},
		{
			name:     "locked during launched (non-gov)",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_LAUNCHED,
			amount:   sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)),
			wantErr:  providertypes.ErrFeePoolLocked,
		},
		{
			// A paused consumer (entered via a successful downtime challenge)
			// locks withdrawals the same way a launched one does, so retro-paid
			// withheld fees can't be raced out of the pool by a depositor.
			name:     "locked during paused (non-gov)",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_PAUSED,
			amount:   sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)),
			wantErr:  providertypes.ErrFeePoolLocked,
		},
		{
			// The gov authority bypasses the lock while paused, same as while
			// launched.
			name:      "gov clawback during paused",
			register:  true,
			phase:     providertypes.CONSUMER_PHASE_PAUSED,
			govSigner: true,
			amount:    sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1_000_000)),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, "uphoton", distrAddr), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, "uphoton"), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
					Return(sdk.NewInt64Coin("uphoton", 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).Return(nil)
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)), providerAddr).Return(nil)
			},
		},
		{
			// alice sole depositor: 100 shares, balance 80.
			// alice asks for 30, partial path: shares_to_burn = 30*100/80 = 37, tokens = 37*80/100 = 29.
			name:     "regular partial withdraw during stopped",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_STOPPED,
			signer:   alice,
			amount:   sdk.NewCoins(sdk.NewInt64Coin("uphoton", 30)),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, "uphoton", alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, "uphoton"), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
					Return(sdk.NewInt64Coin("uphoton", 80))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 29))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providertypes.ModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 29))).Return(nil)
			},
			postCheck: func(t *testing.T, resp *providertypes.MsgWithdrawConsumerFeePoolResponse) {
				require.Equal(t, "29uphoton", resp.Amount.String())
			},
		},
		{
			name:      "gov clawback during launched",
			register:  true,
			phase:     providertypes.CONSUMER_PHASE_LAUNCHED,
			govSigner: true,
			amount:    sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1_000_000)),
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, "uphoton", distrAddr), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, "uphoton"), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
					Return(sdk.NewInt64Coin("uphoton", 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).Return(nil)
				mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
					ctx, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)), providerAddr).Return(nil)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			consumerId := tc.consumerId
			var poolAddr sdk.AccAddress
			if tc.register {
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, tc.phase)
				poolAddr = k.GetConsumerFeePoolAddress(consumerId)
			}

			if tc.setup != nil {
				tc.setup(k, ctx, mocks, consumerId, poolAddr)
			}

			var signer string
			switch {
			case tc.govSigner:
				signer = k.GetAuthority()
			case tc.signer != nil:
				signer = tc.signer.String()
			default:
				signer = depositor.String()
			}

			ms := providerkeeper.NewMsgServerImpl(&k)
			resp, err := ms.WithdrawConsumerFeePool(ctx, &providertypes.MsgWithdrawConsumerFeePool{
				Signer:     signer,
				ConsumerId: consumerId,
				Amount:     tc.amount,
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}

			if tc.postCheck != nil {
				tc.postCheck(t, resp)
			}
		})
	}
}

func TestCreateConsumerUnbondingBounds(t *testing.T) {
	providerUnbonding := 21 * 24 * time.Hour

	tests := []struct {
		name            string
		unbondingPeriod time.Duration
		wantErr         bool
	}{
		{
			name:            "too long: exceeds provider unbonding",
			unbondingPeriod: 30 * 24 * time.Hour,
			wantErr:         true,
		},
		{
			name:            "valid: equal to provider unbonding",
			unbondingPeriod: 21 * 24 * time.Hour,
			wantErr:         false,
		},
		{
			name:            "short value accepted (no lower floor)",
			unbondingPeriod: 1 * 24 * time.Hour,
			wantErr:         false,
		},
		{
			name:            "very short value accepted (no lower floor)",
			unbondingPeriod: time.Hour,
			wantErr:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(providerUnbonding, nil).Times(1)

			_, err := msgServer.CreateConsumer(ctx,
				&providertypes.MsgCreateConsumer{
					Submitter: "submitter",
					ChainId:   "chainId",
					Metadata: providertypes.ConsumerMetadata{
						Name:        "name",
						Description: "description",
					},
					InitializationParameters: &providertypes.ConsumerInitializationParameters{
						UnbondingPeriod: tc.unbondingPeriod,
					},
				})
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, providertypes.ErrInvalidConsumerInitializationParameters)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestCreateConsumerSafeModeThresholdBounds checks that CreateConsumer rejects a
// safe-mode threshold that is not strictly below the provider's liveness grace
// (grace = providerUnbonding * LivenessGraceFraction), so a lagging consumer
// enters safe mode before the provider sweep removes it.
func TestCreateConsumerSafeModeThresholdBounds(t *testing.T) {
	providerUnbonding := 21 * 24 * time.Hour
	// Grace uses the default fraction seeded into params by the test helper.
	grace, err := vaastypes.CalculateTrustPeriod(providerUnbonding, providertypes.DefaultLivenessGraceFraction)
	require.NoError(t, err)

	tests := []struct {
		name      string
		threshold time.Duration
		wantErr   bool
	}{
		{name: "well below grace", threshold: time.Hour, wantErr: false},
		{name: "just below grace", threshold: grace - time.Second, wantErr: false},
		{name: "equal to grace (rejected)", threshold: grace, wantErr: true},
		{name: "above grace (rejected)", threshold: grace + time.Hour, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(providerUnbonding, nil).Times(1)

			_, err := msgServer.CreateConsumer(ctx,
				&providertypes.MsgCreateConsumer{
					Submitter: "submitter",
					ChainId:   "chainId",
					Metadata: providertypes.ConsumerMetadata{
						Name:        "name",
						Description: "description",
					},
					InitializationParameters: &providertypes.ConsumerInitializationParameters{
						UnbondingPeriod:   providerUnbonding,
						SafeModeThreshold: tc.threshold,
					},
				})
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, providertypes.ErrInvalidConsumerInitializationParameters)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSweepConsumerFeePool(t *testing.T) {
	owner := sdk.AccAddress([]byte("owner___________"))
	alice := sdk.AccAddress([]byte("alice___________"))

	testCases := []struct {
		name       string
		register   bool
		consumerId uint64
		phase      providertypes.ConsumerPhase
		setOwner   bool
		signer     sdk.AccAddress
		setup      func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress)
		wantErr    error
	}{
		{
			name:       "unknown consumer",
			register:   false,
			consumerId: 999,
			signer:     owner,
			wantErr:    providertypes.ErrUnknownConsumerId,
		},
		{
			name:     "deleted consumer",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_DELETED,
			setOwner: true,
			signer:   owner,
			wantErr:  providertypes.ErrInvalidPhase,
		},
		{
			name:     "locked during launched",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_LAUNCHED,
			setOwner: true,
			signer:   owner,
			wantErr:  providertypes.ErrFeePoolLocked,
		},
		{
			name:     "locked during paused",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_PAUSED,
			setOwner: true,
			signer:   owner,
			wantErr:  providertypes.ErrFeePoolLocked,
		},
		{
			name:     "non-owner rejected",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_STOPPED,
			setOwner: true,
			signer:   sdk.AccAddress([]byte("not-owner_______")),
			wantErr:  providertypes.ErrUnauthorized,
		},
		{
			name:     "owner triggers sweep",
			register: true,
			phase:    providertypes.CONSUMER_PHASE_STOPPED,
			setOwner: true,
			signer:   owner,
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, consumerId uint64, poolAddr sdk.AccAddress) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, "uphoton", alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, "uphoton"), math.NewInt(100)))
				mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
					Return(sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
					Return(sdk.NewInt64Coin("uphoton", 100))
				mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
					ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).Return(nil)
				mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
					ctx, providertypes.ModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).Return(nil)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			consumerId := tc.consumerId
			var poolAddr sdk.AccAddress
			if tc.register {
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, tc.phase)
				poolAddr = k.GetConsumerFeePoolAddress(consumerId)
			}
			if tc.setOwner {
				k.SetConsumerOwnerAddress(ctx, consumerId, owner.String())
			}

			if tc.setup != nil {
				tc.setup(k, ctx, mocks, consumerId, poolAddr)
			}

			ms := providerkeeper.NewMsgServerImpl(&k)
			_, err := ms.SweepConsumerFeePool(ctx, &providertypes.MsgSweepConsumerFeePool{
				Signer:     tc.signer.String(),
				ConsumerId: consumerId,
				Denoms:     nil,
			})
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestUpdateConsumerUnbondingBounds mirrors TestCreateConsumerUnbondingBounds
// but exercises the UpdateConsumer path for a pre-launch (REGISTERED) consumer.
func TestUpdateConsumerUnbondingBounds(t *testing.T) {
	providerUnbonding := 21 * 24 * time.Hour

	tests := []struct {
		name            string
		unbondingPeriod time.Duration
		wantErr         bool
	}{
		{
			name:            "above provider unbonding: rejected",
			unbondingPeriod: 30 * 24 * time.Hour,
			wantErr:         true,
		},
		{
			name:            "short value accepted (no lower floor)",
			unbondingPeriod: 1 * 24 * time.Hour,
			wantErr:         false,
		},
		{
			name:            "very short value accepted (no lower floor)",
			unbondingPeriod: time.Hour,
			wantErr:         false,
		},
		{
			name:            "exactly provider unbonding: ok",
			unbondingPeriod: providerUnbonding,
			wantErr:         false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			providerKeeper, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			// CreateConsumer calls validateConsumerUnbonding once; UpdateConsumer
			// calls it once more when InitializationParameters is non-nil.
			mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(providerUnbonding, nil).Times(2)
			mocks.MockAccountKeeper.EXPECT().AddressCodec().Return(address.NewBech32Codec("cosmos")).AnyTimes()

			msgServer := providerkeeper.NewMsgServerImpl(&providerKeeper)

			// Register a consumer with a valid unbonding period.
			createResp, err := msgServer.CreateConsumer(ctx, &providertypes.MsgCreateConsumer{
				Submitter: "submitter",
				ChainId:   "chainId",
				Metadata:  providertypes.ConsumerMetadata{Name: "n", Description: "d"},
				InitializationParameters: &providertypes.ConsumerInitializationParameters{
					UnbondingPeriod: 21 * 24 * time.Hour,
				},
			})
			require.NoError(t, err)
			consumerId := createResp.ConsumerId

			// Consumer stays in REGISTERED (pre-launch), so InitializationParameters
			// updates are allowed. Override only UnbondingPeriod.
			_, err = msgServer.UpdateConsumer(ctx, &providertypes.MsgUpdateConsumer{
				Owner:      "submitter",
				ConsumerId: consumerId,
				InitializationParameters: &providertypes.ConsumerInitializationParameters{
					UnbondingPeriod: tc.unbondingPeriod,
				},
			})
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorIs(t, err, providertypes.ErrInvalidConsumerInitializationParameters)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
