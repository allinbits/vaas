package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// TestBeginBlockOrdering_UnresponsiveStopCancelsPendingSlashBeforeSweep encodes
// the module.go BeginBlock call-order contract: SweepUnresponsiveConsumers
// and BeginBlockAutoStopPausedConsumers both run before SweepPendingDowntimeSlashes.
// A launched consumer that goes unresponsive with a matured (but not yet
// executed) pending downtime slash gets stopped by the liveness sweep first;
// StopAndPrepareForConsumerRemoval's CancelConsumerDowntimeState clears that
// pending slash outright. By the time SweepPendingDowntimeSlashes runs later
// in the same block, there is nothing left for it to execute -- no staking
// mock is configured for a slash, so an attempt to execute one would fail the
// test via gomock's unexpected-call panic, not silently pass.
func TestBeginBlockOrdering_UnresponsiveStopCancelsPendingSlashBeforeSweep(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	// The zero-value block time from NewInMemKeeperParams cannot represent a
	// time far enough in the past as a valid protobuf timestamp (see
	// setupSweepTest in downtime_slash_test.go), so anchor to a fixed instant.
	ctx = ctx.WithBlockTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	k.SetInfractionParams(ctx, types.InfractionParameters{
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(5, 1),
		},
	})

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, types.CONSUMER_PHASE_LAUNCHED)
	// Long silent: last ack far enough in the past to exceed the liveness
	// grace period under the default params seeded by the test helper.
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-365*24*time.Hour)))

	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	maturesAt := ctx.BlockTime().Add(-time.Minute) // already matured, ready to execute
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), maturesAt)

	// Mirrors module.go's BeginBlock ordering: liveness sweep and the
	// pause-auto-stop sweep both run before SweepPendingDowntimeSlashes.
	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))

	require.Equal(t, types.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))

	pendingKey := collections.Join(cid, providerAddr.ToSdkConsAddr().Bytes())
	has, err := k.PendingDowntimeSlashes.Has(ctx, pendingKey)
	require.NoError(t, err)
	require.False(t, has, "the matured pending slash must already be cancelled once the consumer auto-stops")

	// No staking mocks are set beyond UnbondingTime: if SweepPendingDowntimeSlashes
	// tried to execute a slash for this (now-cancelled) entry, the unexpected
	// staking call would fail this test.
	require.NotPanics(t, func() {
		k.SweepPendingDowntimeSlashes(ctx)
	})
}
