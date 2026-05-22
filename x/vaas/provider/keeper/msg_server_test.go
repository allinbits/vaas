package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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

func TestFundConsumerFeePool_RegularSigner(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	k.SetParams(ctx, providertypes.DefaultParams())
	params := k.GetParams(ctx)
	params.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, params)

	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	amount := sdk.NewInt64Coin("uphoton", 100)

	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 0))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, alice, providertypes.ModuleName, sdk.NewCoins(amount)).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, poolAddr, sdk.NewCoins(amount)).Return(nil)

	_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
		Signer: alice.String(), ConsumerId: consumerId, Amount: amount,
	})
	require.NoError(t, err)

	shares, _ := k.ConsumerFeePoolShares.Get(ctx,
		collections.Join3(consumerId, "uphoton", alice))
	require.Equal(t, math.NewInt(100), shares)
}

func TestFundConsumerFeePool_GovAuthority(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	k.SetParams(ctx, providertypes.DefaultParams())
	params := k.GetParams(ctx)
	params.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, params)

	govAddr := k.GetAuthority()
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	amount := sdk.NewInt64Coin("uphoton", 1000)

	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 0))
	mocks.MockDistributionKeeper.EXPECT().DistributeFromFeePool(
		ctx, sdk.NewCoins(amount), providerAddr).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, poolAddr, sdk.NewCoins(amount)).Return(nil)

	_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
		Signer: govAddr, ConsumerId: consumerId, Amount: amount,
	})
	require.NoError(t, err)

	shares, _ := k.ConsumerFeePoolShares.Get(ctx,
		collections.Join3(consumerId, "uphoton", distrAddr))
	require.Equal(t, math.NewInt(1000), shares)
}

func TestFundConsumerFeePool_RejectsUnknownConsumer(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)
	alice := sdk.AccAddress([]byte("alice___________"))

	_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
		Signer: alice.String(), ConsumerId: 999,
		Amount: sdk.NewInt64Coin("uphoton", 1),
	})
	require.ErrorIs(t, err, providertypes.ErrUnknownConsumerId)
}

// TestFundConsumerFeePool_AllowedInActivePhases verifies fund is accepted in
// every phase except DELETED (REGISTERED, INITIALIZED, LAUNCHED, STOPPED).
func TestFundConsumerFeePool_AllowedInActivePhases(t *testing.T) {
	allowedPhases := []providertypes.ConsumerPhase{
		providertypes.CONSUMER_PHASE_REGISTERED,
		providertypes.CONSUMER_PHASE_INITIALIZED,
		providertypes.CONSUMER_PHASE_LAUNCHED,
		providertypes.CONSUMER_PHASE_STOPPED,
	}
	for _, phase := range allowedPhases {
		t.Run(phase.String(), func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()
			ms := providerkeeper.NewMsgServerImpl(&k)

			consumerId := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, consumerId, phase)
			k.SetParams(ctx, providertypes.DefaultParams())
			params := k.GetParams(ctx)
			params.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
			k.SetParams(ctx, params)

			alice := sdk.AccAddress([]byte("alice___________"))
			poolAddr := k.GetConsumerFeePoolAddress(consumerId)
			amount := sdk.NewInt64Coin("uphoton", 100)

			mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
				Return(sdk.NewInt64Coin("uphoton", 0))
			mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
				ctx, alice, providertypes.ModuleName, sdk.NewCoins(amount)).Return(nil)
			mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
				ctx, providertypes.ModuleName, poolAddr, sdk.NewCoins(amount)).Return(nil)

			_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
				Signer: alice.String(), ConsumerId: consumerId, Amount: amount,
			})
			require.NoError(t, err)
		})
	}
}

func TestFundConsumerFeePool_RejectsDeleted(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_DELETED)
	alice := sdk.AccAddress([]byte("alice___________"))

	_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
		Signer: alice.String(), ConsumerId: consumerId,
		Amount: sdk.NewInt64Coin("uphoton", 1),
	})
	require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
}

func TestWithdrawConsumerFeePool_Regular(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice sole depositor: 100 shares, balance 80
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", alice), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))

	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 80))
	// alice asks for 30, partial path: shares_to_burn = 30*100/80 = 37, tokens = 37*80/100 = 29.
	// Bank ops execute on a cache-context wrapped around the outer ctx, so match with gomock.Any().
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		gomock.Any(), poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 29))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		gomock.Any(), providertypes.ModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 29))).Return(nil)

	resp, err := ms.WithdrawConsumerFeePool(ctx, &providertypes.MsgWithdrawConsumerFeePool{
		Signer: alice.String(), ConsumerId: consumerId,
		Amount: sdk.NewCoins(sdk.NewInt64Coin("uphoton", 30)),
	})
	require.NoError(t, err)
	require.Equal(t, "29uphoton", resp.Amount.String())
}

func TestWithdrawConsumerFeePool_GovClawback(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", distrAddr), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(100)))

	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 100))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		gomock.Any(), poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100))).Return(nil)
	// Gov clawback: tokens forwarded to community pool via cache-context, not raw bank send.
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		gomock.Any(), sdk.NewCoins(sdk.NewInt64Coin("uphoton", 100)), providerAddr).Return(nil)

	_, err := ms.WithdrawConsumerFeePool(ctx, &providertypes.MsgWithdrawConsumerFeePool{
		Signer: k.GetAuthority(), ConsumerId: consumerId,
		Amount: sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1_000_000)),
	})
	require.NoError(t, err)
}

func TestWithdrawConsumerFeePool_AtomicMultiDenomAbort(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice has shares in uatone (alphabetically first, so iterated first
	// inside the handler) but NOT in uphoton. The first iteration burns
	// shares; the second must fail and the cache-context rollback must
	// restore alice's uatone shares to their pre-call value.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uatone", alice), math.NewInt(50)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uatone"), math.NewInt(50)))

	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), poolAddr, "uatone").
		Return(sdk.NewInt64Coin("uatone", 50)).AnyTimes()

	// Requesting both: uatone succeeds (mutating cached state), uphoton
	// fails (no shares for alice). The handler returns the error and the
	// cache-context is discarded, leaving uatone shares untouched.
	_, err := ms.WithdrawConsumerFeePool(ctx, &providertypes.MsgWithdrawConsumerFeePool{
		Signer: alice.String(), ConsumerId: consumerId,
		Amount: sdk.NewCoins(
			sdk.NewInt64Coin("uatone", 10),
			sdk.NewInt64Coin("uphoton", 10),
		),
	})
	require.Error(t, err)

	// alice's uatone shares should remain 50 (rollback proved)
	s, _ := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, "uatone", alice))
	require.Equal(t, math.NewInt(50), s)
}

func TestSweepConsumerFeePool_OwnerOnly(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	// Use a valid bech32 owner address
	owner := sdk.AccAddress([]byte("owner___________"))
	k.SetConsumerOwnerAddress(ctx, consumerId, owner.String())

	notOwner := sdk.AccAddress([]byte("not-owner_______"))
	_, err := ms.SweepConsumerFeePool(ctx, &providertypes.MsgSweepConsumerFeePool{
		Signer: notOwner.String(), ConsumerId: consumerId, Denoms: nil,
	})
	require.ErrorIs(t, err, providertypes.ErrUnauthorized)
}

func TestSweepConsumerFeePool_OwnerTriggers(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_LAUNCHED)
	owner := sdk.AccAddress([]byte("owner___________"))
	k.SetConsumerOwnerAddress(ctx, consumerId, owner.String())

	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
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

	_, err := ms.SweepConsumerFeePool(ctx, &providertypes.MsgSweepConsumerFeePool{
		Signer: owner.String(), ConsumerId: consumerId, Denoms: nil,
	})
	require.NoError(t, err)
}

func TestFundConsumerFeePool_RejectsWrongDenom(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	ms := providerkeeper.NewMsgServerImpl(&k)

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	k.SetParams(ctx, providertypes.DefaultParams())
	params := k.GetParams(ctx)
	params.FeesPerBlock = sdk.NewInt64Coin("uphoton", 10)
	k.SetParams(ctx, params)
	alice := sdk.AccAddress([]byte("alice___________"))

	_, err := ms.FundConsumerFeePool(ctx, &providertypes.MsgFundConsumerFeePool{
		Signer: alice.String(), ConsumerId: consumerId,
		Amount: sdk.NewInt64Coin("uatone", 1),
	})
	require.ErrorIs(t, err, providertypes.ErrInvalidFundDenom)
}
