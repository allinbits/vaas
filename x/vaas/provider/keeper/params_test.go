package keeper_test

import (
	"testing"
	"time"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// TestParams tests the getting/setting of provider ccv module params.
func TestParams(t *testing.T) {
	// Construct an in-mem keeper with registered key table
	keeperParams := testkeeper.NewInMemKeeperParams(t)
	providerKeeper, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, keeperParams)
	defer ctrl.Finish()

	defaultParams := providertypes.DefaultParams()
	providerKeeper.SetParams(ctx, defaultParams)
	params := providerKeeper.GetParams(ctx)
	require.Equal(t, defaultParams, params)

	newParams := providertypes.NewParams(
		"0.25",
		7*24*time.Hour,
		600,
		10,
		math.NewInt(50),
	)
	providerKeeper.SetParams(ctx, newParams)
	params = providerKeeper.GetParams(ctx)
	require.Equal(t, newParams, params)
}

func TestGetEffectiveFeesPerBlock(t *testing.T) {
	defaultFees := sdk.NewInt64Coin("uphoton", 1000)

	// A zero-value math.Int (IsNil()) in overrideAmount means "no override is set".
	cases := []struct {
		name           string
		overrideOn     uint64
		overrideAmount math.Int
		queryId        uint64
		wantCoin       sdk.Coin
		wantIsOverride bool
	}{
		{
			name:           "no override returns default",
			queryId:        5,
			wantCoin:       defaultFees,
			wantIsOverride: false,
		},
		{
			name:           "override on queried consumer returns override",
			overrideOn:     5,
			overrideAmount: math.NewInt(2500),
			queryId:        5,
			wantCoin:       sdk.NewInt64Coin("uphoton", 2500),
			wantIsOverride: true,
		},
		{
			name:           "override on a different consumer returns default",
			overrideOn:     7,
			overrideAmount: math.NewInt(2500),
			queryId:        5,
			wantCoin:       defaultFees,
			wantIsOverride: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()

			providerParams := providertypes.DefaultParams()
			providerParams.FeesPerBlockAmount = defaultFees.Amount
			k.SetParams(ctx, providerParams)

			if !tc.overrideAmount.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, tc.overrideOn, tc.overrideAmount))
			}

			coin, isOverride := k.GetEffectiveFeesPerBlock(ctx, tc.queryId)
			require.Equal(t, tc.wantIsOverride, isOverride)
			require.Equal(t, tc.wantCoin, coin)
		})
	}
}
