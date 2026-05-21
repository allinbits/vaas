package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	"github.com/stretchr/testify/require"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestComputeClaim(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))

	// No shares yet: claim is zero
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
	require.True(t, k.ComputeClaim(ctx, consumerId, alice, denom).IsZero())

	// Seed: alice has 100 shares of 100 total, balance 50
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 50))
	require.Equal(t, math.NewInt(50), k.ComputeClaim(ctx, consumerId, alice, denom))
}

func TestMintShares_Initial(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Initial deposit: total_shares == 0; mint = amount
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
	require.NoError(t, k.MintShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 100)))

	shares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(100), shares)

	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(100), total)
}

func TestMintShares_Subsequent(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Seed: alice has 100 shares against balance 50 (consumed via fees)
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))

	// Bob deposits 100 when balance is 50 (PRE-deposit) and 150 (POST-deposit)
	// The mint formula uses balance BEFORE the new deposit lands.
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 50))
	require.NoError(t, k.MintShares(ctx, consumerId, bob, sdk.NewInt64Coin(denom, 100)))

	// Bob: shares = 100 * 100 / 50 = 200
	shares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, bob, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(200), shares)

	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(300), total)
}

func TestMintShares_LazyInvalidation(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Seed: alice has 100 shares, balance is 0 (pool fully consumed by fees)
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))

	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))
	require.NoError(t, k.MintShares(ctx, consumerId, bob, sdk.NewInt64Coin(denom, 50)))

	// Alice's shares should be wiped (lazy invalidation), Bob's recorded as initial
	_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)

	bobShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, bob, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(50), bobShares)

	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(50), total)
}

func TestMintShares_SubShareDeposit(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Seed: alice has 1_000_000 shares of 1_000_000 total, balance is huge (1_000_000_000).
	// Bob's tiny 1-unit deposit would mint floor(1 * 1_000_000 / 1_000_000_000) = 0 shares.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(1_000_000)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(1_000_000)))

	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000_000))
	err := k.MintShares(ctx, consumerId, bob, sdk.NewInt64Coin(denom, 1))
	require.ErrorIs(t, err, providertypes.ErrInvalidFundDenom)

	// No state mutation: bob has no entry, total unchanged, alice unchanged.
	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, bob, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)

	aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000), aliceShares)

	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(1_000_000), total)
}

func TestWithdrawShares_Full(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Alice sole depositor with 100 shares against balance 100
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))

	// Request 200 (over claim) — should burn all shares, return claim=100
	tokens, err := k.WithdrawShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 200))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(100), tokens.Amount)

	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)

	_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestWithdrawShares_Partial(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Two depositors, balance 200, total 200
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, bob, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(200)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 200))

	// Alice withdraws 50 (partial, well below claim 100)
	tokens, err := k.WithdrawShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 50))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(50), tokens.Amount)

	// Alice burned 50 shares; total = 150; alice = 50
	aliceShares, _ := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.Equal(t, math.NewInt(50), aliceShares)
	total, _ := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.Equal(t, math.NewInt(150), total)
}

func TestWithdrawShares_Empty(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 0))

	_, err := k.WithdrawShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 50))
	require.ErrorIs(t, err, providertypes.ErrPoolEmpty)
}

func TestWithdrawShares_SubShareGuard(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// Seed: alice has 100 shares of 100 total against a huge balance (1_000_000).
	// Claim = 100 * 1_000_000 / 100 = 1_000_000. A tiny 1-unit withdrawal hits
	// the partial branch and computes sharesToBurn = floor(1 * 100 / 1_000_000) = 0,
	// which must trigger the sub-share guard.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1_000_000))

	tokens, err := k.WithdrawShares(ctx, consumerId, alice, sdk.NewInt64Coin(denom, 1))
	require.ErrorIs(t, err, providertypes.ErrPoolEmpty)
	require.Equal(t, sdk.Coin{}, tokens)

	// No state mutation: alice still has 100 shares, total still 100.
	aliceShares, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(100), aliceShares)

	total, err := k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.NoError(t, err)
	require.Equal(t, math.NewInt(100), total)
}

func TestSweepConsumerFeePoolDenom(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice 30, bob 70, total 100, balance 100 — no dust
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(30)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, bob, denom), math.NewInt(70)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))

	providerModuleName := providertypes.ModuleName
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providerModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin(denom, 30))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providerModuleName, bob, sdk.NewCoins(sdk.NewInt64Coin(denom, 70))).Return(nil)
	// No dust → no FundCommunityPool call

	require.NoError(t, k.SweepConsumerFeePoolDenom(ctx, consumerId, denom))

	// All share records and total cleared
	_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, bob, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestSweepConsumerFeePoolDenom_WithDust(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice 1, bob 2, total 3, balance 10
	// alice claim: floor(1*10/3) = 3; bob claim: floor(2*10/3) = 6; dust = 1
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(1)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, bob, denom), math.NewInt(2)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(3)))

	providerModuleName := providertypes.ModuleName
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 10))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 10))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providerModuleName, alice, sdk.NewCoins(sdk.NewInt64Coin(denom, 3))).Return(nil)
	mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
		ctx, providerModuleName, bob, sdk.NewCoins(sdk.NewInt64Coin(denom, 6))).Return(nil)
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 1)),
		authtypes.NewModuleAddress(providertypes.ModuleName)).Return(nil)

	require.NoError(t, k.SweepConsumerFeePoolDenom(ctx, consumerId, denom))
}

func TestSweepConsumerFeePoolDenom_DistrModuleRecipientUsesCommunityPool(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, distrAddr, denom), math.NewInt(100)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(100)))

	providerModuleName := providertypes.ModuleName
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 100))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).Return(nil)
	// Distribution module account share goes via FundCommunityPool, NOT raw bank send
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 100)),
		authtypes.NewModuleAddress(providertypes.ModuleName)).Return(nil)

	require.NoError(t, k.SweepConsumerFeePoolDenom(ctx, consumerId, denom))
}

func TestSweepConsumerFeePoolDenom_AllFloorToZero(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	denom := "uphoton"
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice 1 share, bob 1 share, total 2, balance 1.
	// alice slice: floor(1*1/2) = 0 → skipped
	// bob slice:   floor(1*1/2) = 0 → skipped
	// distributed = 0; dust = 1 → entire balance routed to community pool.
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, denom), math.NewInt(1)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, bob, denom), math.NewInt(1)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, denom), math.NewInt(2)))

	providerModuleName := providertypes.ModuleName
	mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).Return(sdk.NewInt64Coin(denom, 1))
	mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
		ctx, poolAddr, providerModuleName, sdk.NewCoins(sdk.NewInt64Coin(denom, 1))).Return(nil)
	// No per-holder SendCoinsFromModuleToAccount: every slice floors to zero.
	mocks.MockDistributionKeeper.EXPECT().FundCommunityPool(
		ctx, sdk.NewCoins(sdk.NewInt64Coin(denom, 1)),
		authtypes.NewModuleAddress(providertypes.ModuleName)).Return(nil)

	require.NoError(t, k.SweepConsumerFeePoolDenom(ctx, consumerId, denom))

	// All share records and total cleared.
	_, err := k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, alice, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.ConsumerFeePoolShares.Get(ctx, collections.Join3(consumerId, bob, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.ConsumerFeePoolTotalShares.Get(ctx, collections.Join(consumerId, denom))
	require.ErrorIs(t, err, collections.ErrNotFound)
}

func TestSweepConsumerFeePool_AllDenoms(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := uint64(0)
	alice := sdk.AccAddress([]byte("alice___________"))
	poolAddr := k.GetConsumerFeePoolAddress(consumerId)

	// alice has shares in two denoms
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, "uphoton"), math.NewInt(10)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uphoton"), math.NewInt(10)))
	require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
		collections.Join3(consumerId, alice, "uatone"), math.NewInt(5)))
	require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
		collections.Join(consumerId, "uatone"), math.NewInt(5)))

	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(
		sdk.NewCoins(sdk.NewInt64Coin("uphoton", 10), sdk.NewInt64Coin("uatone", 5)))

	// Two per-denom sweeps. Expect bank ops for each.
	for _, c := range []sdk.Coin{
		sdk.NewInt64Coin("uatone", 5),
		sdk.NewInt64Coin("uphoton", 10),
	} {
		mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, c.Denom).Return(c)
		mocks.MockBankKeeper.EXPECT().SendCoinsFromAccountToModule(
			ctx, poolAddr, providertypes.ModuleName, sdk.NewCoins(c)).Return(nil)
		mocks.MockBankKeeper.EXPECT().SendCoinsFromModuleToAccount(
			ctx, providertypes.ModuleName, alice, sdk.NewCoins(c)).Return(nil)
	}

	require.NoError(t, k.SweepConsumerFeePool(ctx, consumerId, nil))
}
