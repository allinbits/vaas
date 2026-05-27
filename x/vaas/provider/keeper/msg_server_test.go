package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

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

func TestCreateConsumer_PopulatesReverseLookup(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	resp, err := ms.CreateConsumer(ctx, &providertypes.MsgCreateConsumer{
		Submitter: "submitter", ChainId: "chainId",
		Metadata:                 providertypes.ConsumerMetadata{Name: "n", Description: "d"},
		InitializationParameters: &providertypes.ConsumerInitializationParameters{},
	})
	require.NoError(t, err)

	poolAddr := k.GetConsumerFeePoolAddress(resp.ConsumerId)
	consumerId, err := k.FeePoolAddressToConsumerId.Get(ctx, poolAddr)
	require.NoError(t, err)
	require.Equal(t, resp.ConsumerId, consumerId)
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
		amount           sdk.Coin
		setupMocks       func(mocks testkeeper.MockedKeepers, ctx sdk.Context, poolAddr sdk.AccAddress)
		wantErr          error
		postCheck        func(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, consumerId uint64)
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
			params.FeesPerBlock = sdk.NewInt64Coin("uphoton", feesAmount)
			params.MinDepositBlocks = tc.minDepositBlocks
			k.SetParams(ctx, params)

			consumerId := tc.consumerId
			var poolAddr sdk.AccAddress
			if tc.register {
				consumerId = k.FetchAndIncrementConsumerId(ctx)
				k.SetConsumerPhase(ctx, consumerId, tc.phase)
				poolAddr = k.GetConsumerFeePoolAddress(consumerId)
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
