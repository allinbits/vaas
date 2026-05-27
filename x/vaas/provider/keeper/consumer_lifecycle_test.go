package keeper_test

import (
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestDeleteConsumerChain_RemovesReverseLookup(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
	k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())

	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	_, err := k.FeePoolAddressToConsumerId.Get(ctx, poolAddr)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestDeleteConsumerChain_AutoSweep(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0") // required for cleanup block
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
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

	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, k.GetConsumerPhase(ctx, consumerId))
}

// TestDeleteConsumerChain_AutoSweepMultiDenomDust verifies that auto-sweep
// during consumer delete handles multiple denoms with truncation dust
// correctly: each share-holder gets their floor-rounded slice, the residue
// goes to the community pool, and the reverse-lookup is removed.
func TestDeleteConsumerChain_AutoSweepMultiDenomDust(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, consumerId, "07-tendermint-0")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)

	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	providerAddr := authtypes.NewModuleAddress(providertypes.ModuleName)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))

	// uphoton: 3 shares total (alice=1, bob=2) against balance 10 -> alice
	// gets floor(1*10/3)=3, bob gets floor(2*10/3)=6, dust 1.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", alice), math.NewInt(1)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", bob), math.NewInt(2)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(3)))

	// uatone: 7 shares total (alice=4, bob=3) against balance 20 -> alice
	// gets floor(4*20/7)=11, bob gets floor(3*20/7)=8, dust 1.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uatone", alice), math.NewInt(4)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uatone", bob), math.NewInt(3)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uatone"), math.NewInt(7)))

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins(
		sdk.NewInt64Coin("uphoton", 10),
		sdk.NewInt64Coin("uatone", 20),
	))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 10))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uatone").
		Return(sdk.NewInt64Coin("uatone", 20))

	// Per-denom pool drain + per-holder send + dust to community pool.
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providertypes.ModuleName,
		sdk.NewCoins(sdk.NewInt64Coin("uatone", 20))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, alice,
		sdk.NewCoins(sdk.NewInt64Coin("uatone", 11))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, bob,
		sdk.NewCoins(sdk.NewInt64Coin("uatone", 8))).Return(nil)
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		ctx, sdk.NewCoins(sdk.NewInt64Coin("uatone", 1)), providerAddr).Return(nil)

	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providertypes.ModuleName,
		sdk.NewCoins(sdk.NewInt64Coin("uphoton", 10))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, alice,
		sdk.NewCoins(sdk.NewInt64Coin("uphoton", 3))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providertypes.ModuleName, bob,
		sdk.NewCoins(sdk.NewInt64Coin("uphoton", 6))).Return(nil)
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		ctx, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 1)), providerAddr).Return(nil)

	require.NoError(t, k.DeleteConsumerChain(ctx, consumerId))

	require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, k.GetConsumerPhase(ctx, consumerId))

	// Reverse-lookup entry removed.
	_, err := k.FeePoolAddressToConsumerId.Get(ctx, poolAddr)
	require.ErrorIs(t, err, collections.ErrNotFound)

	// All share records cleared.
	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, "uphoton", alice))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, "uatone", bob))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestBeginBlockRemoveConsumers_DeletesEligible verifies that every STOPPED
// consumer whose removal time has elapsed is deleted in the BeginBlocker.
func TestBeginBlockRemoveConsumers_DeletesEligible(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	removalTime := time.Unix(1000, 0)
	ctx = ctx.WithBlockTime(removalTime.Add(time.Hour))

	ids := []uint64{
		k.FetchAndIncrementConsumerId(ctx),
		k.FetchAndIncrementConsumerId(ctx),
	}
	for i, id := range ids {
		k.SetConsumerClientId(ctx, id, fmt.Sprintf("07-tendermint-%d", i))
		k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_STOPPED)
		require.NoError(t, k.SetConsumerRemovalTime(ctx, id, removalTime))
		require.NoError(t, k.AppendConsumerToBeRemoved(ctx, id, removalTime))
		poolAddr := k.GetConsumerFeePoolAddress(id)
		require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, id))
		// Empty pool -> sweep is a no-op (only GetAllBalances is consulted).
		mocks.MockBankKeeper.EXPECT().GetAllBalances(gomock.Any(), poolAddr).Return(sdk.NewCoins())
	}

	require.NoError(t, k.BeginBlockRemoveConsumers(ctx))

	for _, id := range ids {
		require.Equal(t, providertypes.CONSUMER_PHASE_DELETED, k.GetConsumerPhase(ctx, id))
		_, err := k.FeePoolAddressToConsumerId.Get(ctx, k.GetConsumerFeePoolAddress(id))
		require.ErrorIs(t, err, collections.ErrNotFound)
	}
}

// TestDeleteConsumerChain_SweepBankFailurePanics verifies that a bank failure
// during the auto-sweep -- only reachable under state corruption or app
// misconfiguration -- panics rather than silently aborting the delete (which
// would strand the consumer in STOPPED with no recovery path).
func TestDeleteConsumerChain_SweepBankFailurePanics(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	depositor := sdk.AccAddress([]byte("alice___________"))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, "uphoton", depositor), math.NewInt(50)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(50)))

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
		Return(sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50))).
		Return(fmt.Errorf("forced bank error"))

	require.Panics(t, func() {
		_ = k.DeleteConsumerChain(ctx, consumerId)
	})
}
