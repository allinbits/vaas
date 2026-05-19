package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

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
		collections.Join3(consumerId, alice, "uphoton"), math.NewInt(100)))
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

func TestDeleteConsumerChain_AutoSweepFailureAborts(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	// No SetConsumerClientId — sweep fails before the cleanup block runs
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_STOPPED)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
		Return(sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, "uphoton").
		Return(sdk.NewInt64Coin("uphoton", 50))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(sdk.NewInt64Coin("uphoton", 50))).
		Return(fmt.Errorf("forced bank error"))

	err := k.DeleteConsumerChain(ctx, consumerId)
	require.Error(t, err)
	// Phase remains STOPPED, not DELETED
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, consumerId))
}
