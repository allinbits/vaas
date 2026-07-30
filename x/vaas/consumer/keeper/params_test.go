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
	consumerKeeper.SetParams(ctx, vaastypes.DefaultConsumerParams())

	expParams := vaastypes.NewConsumerParams(
		false,
		vaastypes.DefaultVAASTimeoutPeriod,
		vaastypes.DefaultHistoricalEntries,
		vaastypes.DefaultConsumerUnbondingPeriod,
		vaastypes.DefaultSafeModeThreshold,
	) // these are the default params, IBC suite independently sets enabled=true

	params := consumerKeeper.GetConsumerParams(ctx)
	require.Equal(t, expParams, params)

	newParams := vaastypes.NewConsumerParams(false, 7*24*time.Hour, 500, 24*21*time.Hour, vaastypes.DefaultSafeModeThreshold)
	consumerKeeper.SetParams(ctx, newParams)
	params = consumerKeeper.GetConsumerParams(ctx)
	require.Equal(t, newParams, params)

	consumerKeeper.SetUnbondingPeriod(ctx, time.Hour*24*10)
	storedUnbondingPeriod := consumerKeeper.GetUnbondingPeriod(ctx)
	require.Equal(t, time.Hour*24*10, storedUnbondingPeriod)
}

// TestPhotonFeesEnabledParam covers the accessor the photon fee ante decorator
// consults on every transaction: off under the default params, on once the
// param is stored.
func TestPhotonFeesEnabledParam(t *testing.T) {
	consumerKeeper, ctx, ctrl, _ := testkeeper.GetConsumerKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	consumerKeeper.SetParams(ctx, vaastypes.DefaultConsumerParams())
	require.False(t, consumerKeeper.PhotonFeesEnabled(ctx))

	params := vaastypes.DefaultConsumerParams()
	params.PhotonFeesEnabled = true
	consumerKeeper.SetParams(ctx, params)
	require.True(t, consumerKeeper.PhotonFeesEnabled(ctx))
}
