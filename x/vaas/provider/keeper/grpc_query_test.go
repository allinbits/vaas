package keeper_test

import (
	"testing"
	"time"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestQueryConsumerChainIncludesFeePoolAddress(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerId := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerChainId(ctx, consumerId, "consumer-chain")
	k.SetConsumerOwnerAddress(ctx, consumerId, "owner-address")
	k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)
	require.NoError(t, k.SetConsumerMetadata(ctx, consumerId, providertypes.ConsumerMetadata{
		Name: "name", Description: "description", Metadata: "metadata",
	}))

	mocks.MockSlashingKeeper.EXPECT().DowntimeJailDuration(gomock.Any()).Return(time.Hour, nil).AnyTimes()
	mocks.MockSlashingKeeper.EXPECT().SlashFractionDoubleSign(gomock.Any()).Return(math.LegacyNewDec(0), nil).AnyTimes()

	expected := k.GetConsumerFeePoolAddress(consumerId).String()

	res, err := k.QueryConsumerChain(ctx, &providertypes.QueryConsumerChainRequest{ConsumerId: consumerId})
	require.NoError(t, err)
	require.Equal(t, expected, res.FeePoolAddress)

	chain, err := k.GetConsumerChain(ctx, consumerId)
	require.NoError(t, err)
	require.Equal(t, expected, chain.FeePoolAddress)
}

func TestQueryConsumerFeesPerBlock(t *testing.T) {
	defaultFees := sdk.NewInt64Coin("uphoton", 1000)

	// A zero-value math.Int (IsNil()) in overrideAmount means "no override is set".
	cases := []struct {
		name           string
		overrideAmount math.Int
		wantCoin       sdk.Coin
		wantIsOverride bool
	}{
		{
			name:           "no override returns default",
			wantCoin:       defaultFees,
			wantIsOverride: false,
		},
		{
			name:           "override returns the override amount",
			overrideAmount: math.NewInt(2500),
			wantCoin:       sdk.NewInt64Coin("uphoton", 2500),
			wantIsOverride: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			params := testkeeper.NewInMemKeeperParams(t)
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
			defer ctrl.Finish()

			providerParams := providertypes.DefaultParams()
			providerParams.FeesPerBlock = defaultFees
			k.SetParams(ctx, providerParams)

			consumerId := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, consumerId, providertypes.CONSUMER_PHASE_REGISTERED)

			if !tc.overrideAmount.IsNil() {
				require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, consumerId, tc.overrideAmount))
			}

			res, err := k.QueryConsumerFeesPerBlock(ctx, &providertypes.QueryConsumerFeesPerBlockRequest{
				ConsumerId: consumerId,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantIsOverride, res.IsOverride)
			require.Equal(t, tc.wantCoin, res.FeesPerBlock)
		})
	}
}

func TestQueryAllConsumerFeesPerBlockOverrides(t *testing.T) {
	params := testkeeper.NewInMemKeeperParams(t)
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, params)
	defer ctrl.Finish()

	a := k.FetchAndIncrementConsumerId(ctx)
	b := k.FetchAndIncrementConsumerId(ctx)
	c := k.FetchAndIncrementConsumerId(ctx)
	for _, id := range []uint64{a, b, c} {
		k.SetConsumerPhase(ctx, id, providertypes.CONSUMER_PHASE_REGISTERED)
	}
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, a, math.NewInt(500)))
	require.NoError(t, k.ConsumerFeesPerBlockOverride.Set(ctx, c, math.NewInt(700)))

	res, err := k.QueryAllConsumerFeesPerBlockOverrides(ctx, &providertypes.QueryAllConsumerFeesPerBlockOverridesRequest{})
	require.NoError(t, err)
	require.Len(t, res.Overrides, 2)
	// Collection iteration is ordered by uint64 key ascending.
	require.Equal(t, a, res.Overrides[0].ConsumerId)
	require.Equal(t, "500", res.Overrides[0].Amount)
	require.Equal(t, c, res.Overrides[1].ConsumerId)
	require.Equal(t, "700", res.Overrides[1].Amount)
}
