package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"

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

func TestSetConsumerFeesPerBlock(t *testing.T) {
	// Each case starts with a fresh keeper. By default a target consumer in
	// REGISTERED phase is created; setting skipConsumer=true skips that and
	// uses a bogus id (used for the not-found case). A non-nil seedOverride
	// is set on the target consumer before the handler is invoked. An empty
	// overrideAuth resolves to k.GetAuthority() (the canonical gov authority).
	// A nil wantAmount means the override entry should be absent after the
	// call; a non-nil wantAmount asserts the stored value.
	cases := []struct {
		name         string
		overrideAuth string
		skipConsumer bool
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
			name:       "positive amount stored",
			amount:     "2500",
			wantAmount: math.NewInt(2500),
		},
		{
			name:       "zero amount stored",
			amount:     "0",
			wantAmount: math.ZeroInt(),
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

			var consumerId uint64
			if tc.skipConsumer {
				consumerId = 999
			} else {
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
