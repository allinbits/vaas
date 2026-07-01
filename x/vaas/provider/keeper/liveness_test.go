package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	sdk "github.com/cosmos/cosmos-sdk/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
)

func TestConsumerLivenessState(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)

	// lastAck defaults to BlockTime when absent (defensive, not a migration crutch).
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))

	now := ctx.BlockTime().Add(time.Hour)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, now))
	require.Equal(t, now.UTC(), k.GetConsumerLastAckTime(ctx, cid).UTC())

	// counters default to 0.
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestAckedVscId(ctx, cid))
	k.SetConsumerHighestSentVscId(ctx, cid, 5)
	k.SetConsumerHighestAckedVscId(ctx, cid, 3)
	require.Equal(t, uint64(5), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(3), k.GetConsumerHighestAckedVscId(ctx, cid))

	k.DeleteConsumerLastAckTime(ctx, cid)
	k.DeleteConsumerHighestSentVscId(ctx, cid)
	k.DeleteConsumerHighestAckedVscId(ctx, cid)
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
}

func TestOnAckRecordsLiveness(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	future := ctx.BlockTime().Add(2 * time.Hour)
	ctx = ctx.WithBlockTime(future)

	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 9, ""))
	require.Equal(t, future.UTC(), k.GetConsumerLastAckTime(ctx, cid).UTC())
	require.Equal(t, uint64(9), k.GetConsumerHighestAckedVscId(ctx, cid))
}

func TestLivenessGracePeriod(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	// Default fraction (0.66) from the params seeded by the test helper.
	grace, err := k.LivenessGracePeriod(ctx)
	require.NoError(t, err)
	require.Greater(t, grace, time.Duration(0))
	require.Less(t, grace, unbonding) // safety invariant
	require.Equal(t, time.Duration(float64(unbonding)*0.66), grace)

	// The grace tracks the LivenessGraceFraction param: lowering it shortens
	// the grace proportionally (this is what lets e2e/test chains sweep fast).
	params := k.GetParams(ctx)
	params.LivenessGraceFraction = "0.1"
	k.SetParams(ctx, params)

	grace, err = k.LivenessGracePeriod(ctx)
	require.NoError(t, err)
	require.Equal(t, time.Duration(float64(unbonding)*0.1), grace)
}

func TestSweepRemovesStaleConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cid, "consumer-1")

	unbonding := 21 * 24 * time.Hour
	// LivenessGracePeriod + StopAndPrepareForConsumerRemoval both read UnbondingTime.
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	// Last ack far in the past -> beyond grace.
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-30*24*time.Hour)))

	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))

	// The sweep does not just stop the consumer, it schedules its deletion at
	// blockTime + unbonding (the same removal path a gov removal takes). The
	// e2e suite asserts the LAUNCHED -> STOPPED edge end-to-end and relies on
	// this assertion for the STOPPED -> DELETED scheduling.
	removalTime, err := k.GetConsumerRemovalTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(unbonding), removalTime)
}

func TestSweepSparesLiveConsumer(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime())) // fresh
	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestTimeoutDoesNotRemove(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.OnTimeoutPacketV2(ctx, clientId))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestErrorAckDoesNotRemove(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const cid = uint64(0)
	clientId := "07-tendermint-0"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 1, "some error"))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

func TestDeleteConsumerChainClearsLivenessState(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	const cid = uint64(0)
	poolAddr := k.GetConsumerFeePoolAddress(cid)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, cid))
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())

	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime()))
	k.SetConsumerHighestSentVscId(ctx, cid, 4)
	k.SetConsumerHighestAckedVscId(ctx, cid, 4)

	// confirm keys are present before deletion
	hasAck, err := k.ConsumerLastAckTime.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasAck)
	hasSent, err := k.ConsumerHighestSentVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasSent)
	hasAcked, err := k.ConsumerHighestAckedVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.True(t, hasAcked)

	require.NoError(t, k.DeleteConsumerChain(ctx, cid))

	// assert underlying keys are gone, not just that the getter returns a default
	hasAck, err = k.ConsumerLastAckTime.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasAck)
	hasSent, err = k.ConsumerHighestSentVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasSent)
	hasAcked, err = k.ConsumerHighestAckedVscId.Has(ctx, cid)
	require.NoError(t, err)
	require.False(t, hasAcked)

	// getters return their absent-defaults after deletion
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestAckedVscId(ctx, cid))
}

// TestSweepBoundaryExact checks the boundary condition: lastAck exactly at the
// grace boundary is spared; one nanosecond earlier is stopped.
func TestSweepBoundaryExact(t *testing.T) {
	unbonding := 21 * 24 * time.Hour

	t.Run("exactly at grace boundary - spared", func(t *testing.T) {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()
		grace, err := k.LivenessGracePeriod(ctx)
		require.NoError(t, err)

		cid := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

		// lastAck == blockTime - grace: elapsed == grace, check is <= grace, so spared.
		require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-grace)))
		require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
		require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	})

	t.Run("one ns past grace - stopped", func(t *testing.T) {
		k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
		defer ctrl.Finish()

		mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()
		grace, err := k.LivenessGracePeriod(ctx)
		require.NoError(t, err)

		cid := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
		k.SetConsumerChainId(ctx, cid, "consumer-boundary")

		// lastAck == blockTime - grace - 1ns: elapsed > grace, should be stopped.
		require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-grace-time.Nanosecond)))
		require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
		require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))
	})
}

// TestSweepMultipleConsumers_PartialRemoval ensures only the stale consumer is
// stopped when three consumers are present: stale, fresh, and at-grace-boundary.
func TestSweepMultipleConsumers_PartialRemoval(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	grace, err := k.LivenessGracePeriod(ctx)
	require.NoError(t, err)

	// stale: elapsed >> grace.
	cidStale := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cidStale, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerChainId(ctx, cidStale, "consumer-stale")
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cidStale, ctx.BlockTime().Add(-30*24*time.Hour)))

	// fresh: lastAck == blockTime (elapsed = 0).
	cidFresh := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cidFresh, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cidFresh, ctx.BlockTime()))

	// at boundary: elapsed == grace -> spared (check is <= grace).
	cidBoundary := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cidBoundary, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cidBoundary, ctx.BlockTime().Add(-grace)))

	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))

	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cidStale))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cidFresh))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cidBoundary))

	// The sweep is per-consumer: only the stale one is scheduled for removal;
	// the spared consumers are left entirely untouched (no removal time).
	staleRemoval, err := k.GetConsumerRemovalTime(ctx, cidStale)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(unbonding), staleRemoval)
	_, err = k.GetConsumerRemovalTime(ctx, cidFresh)
	require.Error(t, err)
	_, err = k.GetConsumerRemovalTime(ctx, cidBoundary)
	require.Error(t, err)
}

// TestSweepContinuesAfterStopError is SKIPPED.
//
// StopAndPrepareForConsumerRemoval sets the consumer phase to STOPPED unconditionally
// before it calls stakingKeeper.UnbondingTime. There is no mock lever that causes
// the stop to fail while leaving the consumer in LAUNCHED: by the time UnbondingTime
// is called, the phase change has already been committed to the store. Injecting an
// error via UnbondingTime only prevents the removal-time entry from being written, but
// the consumer phase is already STOPPED. A faithful per-consumer stop failure test
// would require refactoring production code, which is out of scope here.

// TestSweepRecoversAfterAck confirms a consumer that receives a fresh ack just
// inside the grace window survives a subsequent sweep once more time elapses.
func TestSweepRecoversAfterAck(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	grace, err := k.LivenessGracePeriod(ctx)
	require.NoError(t, err)

	cid := k.FetchAndIncrementConsumerId(ctx)
	clientId := "07-tendermint-recover"
	k.SetConsumerClientId(ctx, cid, clientId)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	// Set lastAck to just inside the grace window (1ns before expiry).
	require.NoError(t, k.SetConsumerLastAckTime(ctx, cid, ctx.BlockTime().Add(-grace+time.Nanosecond)))

	// Simulate ack arriving at current block time, refreshing the liveness clock.
	require.NoError(t, k.OnAcknowledgementPacketV2(ctx, clientId, 1, ""))

	// Advance block time by exactly grace; elapsed == grace which satisfies <= grace, so spared.
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(grace))

	require.NoError(t, k.SweepUnresponsiveConsumers(ctx))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}
