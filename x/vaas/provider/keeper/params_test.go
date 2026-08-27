package keeper_test

import (
	"testing"
	"time"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
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
		"0.5",
		7*24*time.Hour,
		600,
		math.NewInt(50),
		providertypes.DefaultMinDepositBlocks,
		providertypes.DefaultMaxPauseDuration,
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

// TestAcceptableDowntimeParams verifies that evidence echoing the current
// downtime params is always accepted, evidence echoing the immediately-prior
// params is accepted only within the evidence-max-age + challenge-window
// grace horizon, and evidence echoing anything else is rejected.
func TestAcceptableDowntimeParams(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	initial := providertypes.DefaultInfractionParameters()
	k.SetInfractionParams(ctx, initial)

	oldDowntimeParams := k.CurrentDowntimeParams(ctx)

	// current accepted
	require.True(t, k.AcceptableDowntimeParams(ctx, oldDowntimeParams))

	// change the downtime window; the old values move into PreviousDowntimeParams
	// stamped with the current block time.
	changed := initial
	changed.SignedBlocksWindow = 1000
	k.SetInfractionParams(ctx, changed)

	newDowntimeParams := k.CurrentDowntimeParams(ctx)
	require.NotEqual(t, oldDowntimeParams.SignedBlocksWindow, newDowntimeParams.SignedBlocksWindow)

	// new current still accepted
	require.True(t, k.AcceptableDowntimeParams(ctx, newDowntimeParams))

	horizon := initial.DowntimeEvidenceMaxAge + initial.DowntimeChallengeWindow

	// previous accepted within the grace horizon
	withinHorizon := ctx.WithBlockTime(ctx.BlockTime().Add(horizon - time.Hour))
	require.True(t, k.AcceptableDowntimeParams(withinHorizon, oldDowntimeParams))

	// previous rejected once the grace horizon has elapsed
	afterHorizon := ctx.WithBlockTime(ctx.BlockTime().Add(horizon + time.Hour))
	require.False(t, k.AcceptableDowntimeParams(afterHorizon, oldDowntimeParams))

	// an unrelated value is never accepted
	unknown := vaastypes.DowntimeParams{
		SignedBlocksWindow: 42,
		MinSignedPerWindow: math.LegacyMustNewDecFromStr("0.5"),
	}
	require.False(t, k.AcceptableDowntimeParams(withinHorizon, unknown))
}
