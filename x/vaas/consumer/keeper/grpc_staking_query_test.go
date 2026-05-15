package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	consumerkeeper "github.com/allinbits/vaas/x/vaas/consumer/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestStakingQueryServerParams asserts that the staking.Params query handler
// exposed by the consumer module:
//   - reflects the consumer's UnbondingPeriod and HistoricalEntries from
//     ConsumerParams, which IBC relayers rely on to derive trusting_period;
//   - leaves BondDenom, MaxValidators, MaxEntries, and MinCommissionRate at
//     their zero values, since VAAS consumer chains have no local bonding,
//     validator selection, or commission.
func TestStakingQueryServerParams(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerKeeper.SetParams(ctx, vaastypes.NewParams(
		true,
		vaastypes.DefaultVAASTimeoutPeriod,
		1234,
		13*24*time.Hour,
	))

	server := consumerkeeper.NewStakingQueryServer(&consumerKeeper)
	resp, err := server.Params(ctx, &stakingtypes.QueryParamsRequest{})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Real, VAAS-owned values pass through.
	require.Equal(t, 13*24*time.Hour, resp.Params.UnbondingTime)
	require.Equal(t, uint32(1234), resp.Params.HistoricalEntries)

	// Fields that have no meaning on a consumer chain must not be fabricated.
	require.Equal(t, "", resp.Params.BondDenom)
	require.Equal(t, uint32(0), resp.Params.MaxValidators)
	require.Equal(t, uint32(0), resp.Params.MaxEntries)
	require.True(t, resp.Params.MinCommissionRate.Equal(math.LegacyZeroDec()))
}
