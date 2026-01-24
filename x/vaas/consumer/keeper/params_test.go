package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestParams tests getters/setters for consumer params
func TestParams(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	consumerKeeper.SetParams(ctx, vaastypes.DefaultParams())

	expParams := vaastypes.NewParams(
		false,
		vaastypes.DefaultVAASTimeoutPeriod,
		vaastypes.DefaultHistoricalEntries,
		vaastypes.DefaultConsumerUnbondingPeriod,
	) // these are the default params, IBC suite independently sets enabled=true

	params := consumerKeeper.GetConsumerParams(ctx)
	require.Equal(t, expParams, params)

	newParams := vaastypes.NewParams(false, 7*24*time.Hour, 500, 24*21*time.Hour)
	consumerKeeper.SetParams(ctx, newParams)
	params = consumerKeeper.GetConsumerParams(ctx)
	require.Equal(t, newParams, params)

	consumerKeeper.SetUnbondingPeriod(ctx, time.Hour*24*10)
	storedUnbondingPeriod := consumerKeeper.GetUnbondingPeriod(ctx)
	require.Equal(t, time.Hour*24*10, storedUnbondingPeriod)
}
