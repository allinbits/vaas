package keeper_test

// invariants_test.go tests FeePoolSharesConsistencyInvariant directly.
//
// x/crisis is not wired into either app in this repository, so the registered
// invariant route never runs in-app and cannot be reached through a message or
// a block. The invariant still encodes the fee-pool share accounting contract
// (see x/vaas/provider/keeper/fee_pool_shares.go), so it is exercised here by
// calling it against state written straight through the keeper's collections:
// one case per violation class it reports, plus a clean-state case that must
// report nothing.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
)

func TestFeePoolSharesConsistencyInvariant(t *testing.T) {
	const (
		consumerId = uint64(0)
		denom      = "uphoton"
	)
	alice := sdk.AccAddress([]byte("alice___________"))
	bob := sdk.AccAddress([]byte("bob_____________"))

	testCases := []struct {
		name string
		// setup writes the state under test and registers whatever bank
		// expectations that state makes the invariant perform.
		setup func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers)
		// wantBroken is the invariant's own boolean verdict; wantMsg, when
		// non-empty, must appear in the reported violation message.
		wantBroken bool
		wantMsg    string
	}{
		{
			name:       "empty state is consistent",
			setup:      func(providerkeeper.Keeper, sdk.Context, testkeeper.MockedKeepers) {},
			wantBroken: false,
		},
		{
			name: "shares summing to the stored total with a funded pool is consistent",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(60)))
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, bob), math.NewInt(40)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				poolAddr := k.GetConsumerFeePoolAddress(consumerId)
				require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
					Return(sdk.NewInt64Coin(denom, 100)).AnyTimes()
				mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
					Return(sdk.NewCoins(sdk.NewInt64Coin(denom, 100))).AnyTimes()
			},
			wantBroken: false,
		},
		{
			name: "share rows not summing to the stored total is a violation",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(60)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				poolAddr := k.GetConsumerFeePoolAddress(consumerId)
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
					Return(sdk.NewInt64Coin(denom, 100)).AnyTimes()
			},
			wantBroken: true,
			wantMsg:    "sum(shares)=60 != total=100",
		},
		{
			name: "a total row with no share rows is a violation",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				poolAddr := k.GetConsumerFeePoolAddress(consumerId)
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
					Return(sdk.NewInt64Coin(denom, 100)).AnyTimes()
			},
			wantBroken: true,
			wantMsg:    "has no share records",
		},
		{
			name: "share rows with no total row is a violation",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, _ testkeeper.MockedKeepers) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(60)))
			},
			wantBroken: true,
			wantMsg:    "without stored total",
		},
		{
			name: "a positive total against an empty pool is a violation",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				require.NoError(t, k.ConsumerFeePoolShares.Set(ctx,
					collections.Join3(consumerId, denom, alice), math.NewInt(100)))
				require.NoError(t, k.ConsumerFeePoolTotalShares.Set(ctx,
					collections.Join(consumerId, denom), math.NewInt(100)))

				poolAddr := k.GetConsumerFeePoolAddress(consumerId)
				mocks.MockBankKeeper.EXPECT().GetBalance(ctx, poolAddr, denom).
					Return(sdk.NewInt64Coin(denom, 0)).AnyTimes()
			},
			wantBroken: true,
			wantMsg:    "pool balance is zero",
		},
		{
			name: "a funded pool with no shares at all is a violation",
			setup: func(k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers) {
				poolAddr := k.GetConsumerFeePoolAddress(consumerId)
				require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, consumerId))
				mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).
					Return(sdk.NewCoins(sdk.NewInt64Coin(denom, 7))).AnyTimes()
			},
			wantBroken: true,
			wantMsg:    "holds 7 with no shares",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			tc.setup(k, ctx, mocks)

			msg, broken := providerkeeper.FeePoolSharesConsistencyInvariant(k)(ctx)
			require.Equal(t, tc.wantBroken, broken, "invariant verdict mismatch; message: %s", msg)
			if tc.wantMsg != "" {
				require.Contains(t, msg, tc.wantMsg)
			}
			if !tc.wantBroken {
				require.Empty(t, msg)
			}
		})
	}
}
