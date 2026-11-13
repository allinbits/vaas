package keeper_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/codec/address"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// TestCancelConsumerDowntimeState_ClearsPendingSlashesAndEpochMarks verifies
// that CancelConsumerDowntimeState deletes only the target consumer's pending
// downtime slashes and epoch downtime marks, leaving another consumer's state
// untouched.
func TestCancelConsumerDowntimeState_ClearsPendingSlashesAndEpochMarks(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	const consumerA, consumerB = uint64(1), uint64(2)
	providerAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))

	putPendingDowntimeSlash(t, k, ctx, consumerA, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	putPendingDowntimeSlash(t, k, ctx, consumerB, providerAddr, math.NewInt(200), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, consumerA, providerAddr.ToSdkConsAddr())
	k.MarkEpochDowntime(ctx, consumerB, providerAddr.ToSdkConsAddr())

	require.NoError(t, k.CancelConsumerDowntimeState(ctx, consumerA))

	_, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerA, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.False(t, k.IsEpochDowntime(ctx, consumerA, providerAddr.ToSdkConsAddr()))

	// consumerB's state is untouched.
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(consumerB, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err)
	require.True(t, k.IsEpochDowntime(ctx, consumerB, providerAddr.ToSdkConsAddr()))
}

// TestPauseConsumerChain_RequiresLaunched verifies that only a launched
// consumer can be paused.
func TestPauseConsumerChain_RequiresLaunched(t *testing.T) {
	for _, phase := range []providertypes.ConsumerPhase{
		providertypes.CONSUMER_PHASE_REGISTERED,
		providertypes.CONSUMER_PHASE_INITIALIZED,
		providertypes.CONSUMER_PHASE_STOPPED,
		providertypes.CONSUMER_PHASE_PAUSED,
		providertypes.CONSUMER_PHASE_DELETED,
	} {
		t.Run(phase.String(), func(t *testing.T) {
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			cid := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, cid, phase)

			err := k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE)
			require.Error(t, err)
			require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
			require.Equal(t, phase, k.GetConsumerPhase(ctx, cid))
		})
	}
}

// TestPauseConsumerChain_Success verifies that pausing a launched consumer:
// sets the phase to PAUSED, cancels its pending downtime state, schedules an
// auto-stop at now + MaxPauseDuration, and emits the pause event.
func TestPauseConsumerChain_Success(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr())

	maxPause := k.GetMaxPauseDuration(ctx)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))

	// downtime state cancelled
	_, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.False(t, k.IsEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr()))

	// auto-stop scheduled at now + MaxPauseDuration
	wantExpiration := ctx.BlockTime().Add(maxPause)
	gotExpiration, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, wantExpiration, gotExpiration)

	queued, err := k.GetConsumersToBeAutoStopped(ctx, wantExpiration)
	require.NoError(t, err)
	require.Equal(t, []uint64{cid}, queued.Ids)

	// pause event emitted
	var found bool
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != "vaas_consumer_paused" {
			continue
		}
		found = true
		var sawConsumerId bool
		for _, attr := range ev.Attributes {
			if attr.Key == "consumer_id" && attr.Value == "0" {
				sawConsumerId = true
			}
		}
		require.True(t, sawConsumerId, "consumer_id attribute missing on pause event")
	}
	require.True(t, found, "vaas_consumer_paused event not emitted")
}

// TestBeginBlockAutoStopPausedConsumers_StopsMaturedPause verifies that a
// paused consumer whose MaxPauseDuration has elapsed is transitioned to
// STOPPED (not deleted directly) and has its removal scheduled, mirroring
// SweepUnresponsiveConsumers.
func TestBeginBlockAutoStopPausedConsumers_StopsMaturedPause(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))

	maxPause := k.GetMaxPauseDuration(ctx)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(maxPause + time.Nanosecond))

	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))

	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))

	removalTime, err := k.GetConsumerRemovalTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(unbonding), removalTime)

	// the pause-expiration bookkeeping is cleared once the consumer stops
	_, err = k.GetConsumerPauseExpirationTime(ctx, cid)
	require.Error(t, err)
}

// TestBeginBlockAutoStopPausedConsumers_StopsAtExactExpirationTime pins the
// boundary semantics of the shared time queue: ConsumeIdsFromTimeQueue skips
// only entries whose time is strictly after the block time, so a pause whose
// expiration equals the block time exactly is already matured and the
// auto-stop fires at equality, not one nanosecond later.
func TestBeginBlockAutoStopPausedConsumers_StopsAtExactExpirationTime(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	expiration, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	ctx = ctx.WithBlockTime(expiration)

	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))

	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, cid))
	_, err = k.GetConsumerPauseExpirationTime(ctx, cid)
	require.Error(t, err, "the pause-expiration bookkeeping must be cleared by the boundary stop")
}

// TestBeginBlockAutoStopPausedConsumers_SkipsUnexpiredPause verifies that a
// pause whose auto-stop time has not yet arrived is left untouched.
func TestBeginBlockAutoStopPausedConsumers_SkipsUnexpiredPause(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	// Still well before now + MaxPauseDuration.
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))

	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
}

// TestBeginBlockAutoStopPausedConsumers_SkipsNoLongerPaused verifies the
// defensive phase re-check: if a consumer's pause matures in the queue but
// the consumer is no longer PAUSED (e.g. some other path already moved it),
// the sweep does not attempt to stop it.
func TestBeginBlockAutoStopPausedConsumers_SkipsNoLongerPaused(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	// Simulate the consumer having left PAUSED through some other path before
	// its auto-stop matured.
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	maxPause := k.GetMaxPauseDuration(ctx)
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(maxPause + time.Nanosecond))

	// No UnbondingTime mock is set: if StopAndPrepareForConsumerRemoval were
	// invoked, the test would fail on the unexpected call.
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(ctx))

	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
}

// TestBeginBlockAutoStopPausedConsumers_WaitsForTheForkWaitOfALightClientPause:
// a light-client pause whose MaxPauseDuration runs out before its fork wait
// is not stopped, since a resume is refused until the wait has run (see
// ResumeConsumerChain) and the stop would have made the pause terminal; its
// auto-stop moves to the end of the wait and fires there. A pause moved under
// the light-client reason by a later freeze waits from the freeze.
func TestBeginBlockAutoStopPausedConsumers_WaitsForTheForkWaitOfALightClientPause(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	params := k.GetParams(ctx)
	params.MaxPauseDuration = time.Hour
	k.SetParams(ctx, params)
	clientState := &ibctmtypes.ClientState{TrustingPeriod: 14 * 24 * time.Hour, MaxClockDrift: 10 * time.Second}
	wait := clientState.TrustingPeriod + clientState.MaxClockDrift

	// Paused for a fork found on the consumer: the wait counts from the pause.
	forked := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, forked, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, forked, "07-tendermint-0")
	pausedAt := ctx.BlockTime()
	require.NoError(t, k.PauseConsumerChain(ctx, forked, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))

	// Paused for a refusal and frozen half an hour in: the wait counts from
	// the freeze.
	escalated := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, escalated, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, escalated, "07-tendermint-1")
	require.NoError(t, k.PauseConsumerChain(ctx, escalated, providertypes.PAUSE_REASON_REFUSAL_THRESHOLD))
	frozenAt := pausedAt.Add(30 * time.Minute)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Frozen).Times(1)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-1").Return(ibcexported.Frozen).Times(1)
	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(frozenAt))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, escalated))

	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-0").Return(clientState, true).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-1").Return(clientState, true).AnyTimes()

	// MaxPauseDuration runs out: both stay paused, their auto-stops moved to
	// the ends of their waits.
	lapsed := ctx.WithBlockTime(pausedAt.Add(time.Hour))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(lapsed))
	for _, c := range []struct {
		id          uint64
		resumableAt time.Time
	}{{forked, pausedAt.Add(wait)}, {escalated, frozenAt.Add(wait)}} {
		require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(lapsed, c.id))
		expiration, err := k.GetConsumerPauseExpirationTime(lapsed, c.id)
		require.NoError(t, err)
		require.Equal(t, c.resumableAt, expiration, "the auto-stop waits for the resume to become possible")
		queued, err := k.GetConsumersToBeAutoStopped(lapsed, c.resumableAt)
		require.NoError(t, err)
		require.Equal(t, []uint64{c.id}, queued.Ids)
	}

	// The waits end: the first consumer stops at the end of its wait, the
	// escalated one half an hour later.
	atForkedWait := ctx.WithBlockTime(pausedAt.Add(wait))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(atForkedWait))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(atForkedWait, forked))
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(atForkedWait, escalated))
	atEscalatedWait := ctx.WithBlockTime(frozenAt.Add(wait))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(atEscalatedWait))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(atEscalatedWait, escalated))
}

// TestBeginBlockAutoStopPausedConsumers_BoundsTheForkWaitByTheProviderUnbondingPeriod:
// the deferral of a light-client pause's auto-stop never goes past one
// provider unbonding period after the fork became known, whatever trusting
// period and clock-drift allowance the client carries (a client substituted
// by governance need not fit within it, unlike a declared one), and a drift
// that would wrap their sum past the range of a duration lands on the same
// cap rather than on a time in the past.
func TestBeginBlockAutoStopPausedConsumers_BoundsTheForkWaitByTheProviderUnbondingPeriod(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	unbonding := 21 * 24 * time.Hour
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(unbonding, nil).AnyTimes()

	params := k.GetParams(ctx)
	params.MaxPauseDuration = time.Hour
	k.SetParams(ctx, params)
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-0").
		Return(&ibctmtypes.ClientState{TrustingPeriod: 30 * 24 * time.Hour, MaxClockDrift: 10 * time.Second}, true).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-1").
		Return(&ibctmtypes.ClientState{TrustingPeriod: 20 * 24 * time.Hour, MaxClockDrift: time.Duration(1<<63 - 1)}, true).AnyTimes()

	pausedAt := ctx.BlockTime()
	var cids []uint64
	for _, clientId := range []string{"07-tendermint-0", "07-tendermint-1"} {
		cid := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
		k.SetConsumerClientId(ctx, cid, clientId)
		require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
		cids = append(cids, cid)
	}

	lapsed := ctx.WithBlockTime(pausedAt.Add(time.Hour))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(lapsed))
	for _, cid := range cids {
		require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(lapsed, cid))
		expiration, err := k.GetConsumerPauseExpirationTime(lapsed, cid)
		require.NoError(t, err)
		require.Equal(t, pausedAt.Add(unbonding), expiration, "the deferral is capped at the provider unbonding period")
	}

	atCap := ctx.WithBlockTime(pausedAt.Add(unbonding))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(atCap))
	for _, cid := range cids {
		require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(atCap, cid))
	}
}

// TestBeginBlockAutoStopPausedConsumers_StopsALightClientPauseWithoutAClientState:
// a light-client pause with no declared client, or with a client whose state
// cannot be read, has no fork wait to measure and stops when MaxPauseDuration
// runs out, like any other pause.
func TestBeginBlockAutoStopPausedConsumers_StopsALightClientPauseWithoutAClientState(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	params := k.GetParams(ctx)
	params.MaxPauseDuration = time.Hour
	k.SetParams(ctx, params)

	undeclared := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, undeclared, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, undeclared, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))

	unreadable := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, unreadable, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, unreadable, "07-tendermint-2")
	require.NoError(t, k.PauseConsumerChain(ctx, unreadable, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-2").Return(nil, false).AnyTimes()

	lapsed := ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))
	require.NoError(t, k.BeginBlockAutoStopPausedConsumers(lapsed))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(lapsed, undeclared))
	require.Equal(t, providertypes.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(lapsed, unreadable))
}

// TestStopAndPrepareForConsumerRemoval_CancelsDowntimeState verifies that
// stopping a consumer (whether from the liveness sweep or elsewhere) cancels
// any pending downtime state and drops its withheld fee records, which a
// stopped consumer could never pay out and which would otherwise cap
// depositor withdrawals until they expired, while the accepted-window records
// and the pruned floor deliberately survive: a cancelled window must not
// become re-submittable, so only DeleteConsumerChain erases them.
func TestStopAndPrepareForConsumerRemoval_CancelsDowntimeState(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	providerAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	pairKey := collections.Join(cid, providerAddr.ToSdkConsAddr().Bytes())
	acceptedKey := collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100))
	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr())
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, acceptedKey, providertypes.AcceptedDowntimeWindow{
		WindowStart: 93,
		AcceptedAt:  ctx.BlockTime(),
	}))
	require.NoError(t, k.DowntimeWindowFloors.Set(ctx, pairKey, 50))
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, pairKey, providertypes.WithheldFeeRecord{
		ConsumerId:       cid,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))
	// Another consumer's record is out of the stop's range.
	otherCid := k.FetchAndIncrementConsumerId(ctx)
	otherPairKey := collections.Join(otherCid, providerAddr.ToSdkConsAddr().Bytes())
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, otherPairKey, providertypes.WithheldFeeRecord{
		ConsumerId:       otherCid,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 75),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))

	require.NoError(t, k.StopAndPrepareForConsumerRemoval(ctx, cid))

	_, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.False(t, k.IsEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr()))
	_, err = k.WithheldFeeRecords.Get(ctx, pairKey)
	require.ErrorIs(t, err, collections.ErrNotFound, "withheld fee records must be dropped by the stop")
	_, err = k.WithheldFeeRecords.Get(ctx, otherPairKey)
	require.NoError(t, err, "another consumer's withheld fee record must survive")

	accepted, err := k.AcceptedDowntimeWindows.Get(ctx, acceptedKey)
	require.NoError(t, err, "accepted downtime window must survive the stop")
	require.Equal(t, int64(93), accepted.WindowStart)
	floor, err := k.DowntimeWindowFloors.Get(ctx, pairKey)
	require.NoError(t, err, "downtime window floor must survive the stop")
	require.Equal(t, int64(50), floor)
}

// TestDeleteConsumerChain_ClearsDowntimeAndWithheldState verifies that
// deleting a consumer erases every downtime-related record for it: pending
// downtime slashes, epoch downtime marks, withheld fee records, accepted
// downtime windows, and downtime window floors -- and that the range-deletes
// are scoped to the deleted consumer id only, leaving another consumer's
// records untouched.
func TestDeleteConsumerChain_ClearsDowntimeAndWithheldState(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)

	poolAddr := k.GetConsumerFeePoolAddress(cid)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, cid))
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())

	providerAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	pairKey := collections.Join(cid, providerAddr.ToSdkConsAddr().Bytes())

	putPendingDowntimeSlash(t, k, ctx, cid, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr())
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, pairKey, providertypes.WithheldFeeRecord{
		ConsumerId:       cid,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 50),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))
	acceptedKey := collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100))
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, acceptedKey, providertypes.AcceptedDowntimeWindow{
		WindowStart: 93,
		AcceptedAt:  ctx.BlockTime(),
	}))
	require.NoError(t, k.DowntimeWindowFloors.Set(ctx, pairKey, 50))

	// A second, untouched consumer with its own withheld-fee record,
	// accepted-window entry, and floor, kept in a phase that
	// DeleteConsumerChain would reject (only STOPPED can be deleted), so it
	// is never itself deleted here -- it exists purely to prove the
	// range-delete on cid does not bleed into other consumer ids.
	otherCid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, otherCid, providertypes.CONSUMER_PHASE_LAUNCHED)
	otherPairKey := collections.Join(otherCid, providerAddr.ToSdkConsAddr().Bytes())
	require.NoError(t, k.WithheldFeeRecords.Set(ctx, otherPairKey, providertypes.WithheldFeeRecord{
		ConsumerId:       otherCid,
		ProviderConsAddr: providerAddr.ToSdkConsAddr().Bytes(),
		Amount:           sdk.NewInt64Coin("uphoton", 75),
		ExpiresAt:        ctx.BlockTime().Add(time.Hour),
	}))
	otherAcceptedKey := collections.Join3(otherCid, providerAddr.ToSdkConsAddr().Bytes(), int64(200))
	require.NoError(t, k.AcceptedDowntimeWindows.Set(ctx, otherAcceptedKey, providertypes.AcceptedDowntimeWindow{
		WindowStart: 193,
		AcceptedAt:  ctx.BlockTime(),
	}))
	require.NoError(t, k.DowntimeWindowFloors.Set(ctx, otherPairKey, 150))

	require.NoError(t, k.DeleteConsumerChain(ctx, cid))

	_, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.False(t, k.IsEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr()))
	_, err = k.WithheldFeeRecords.Get(ctx, pairKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.AcceptedDowntimeWindows.Get(ctx, acceptedKey)
	require.ErrorIs(t, err, collections.ErrNotFound)
	_, err = k.DowntimeWindowFloors.Get(ctx, pairKey)
	require.ErrorIs(t, err, collections.ErrNotFound)

	// The other consumer's records must survive untouched.
	otherWithheld, err := k.WithheldFeeRecords.Get(ctx, otherPairKey)
	require.NoError(t, err, "unrelated consumer's withheld fee record must not be deleted")
	require.Equal(t, sdk.NewInt64Coin("uphoton", 75), otherWithheld.Amount)
	otherAccepted, err := k.AcceptedDowntimeWindows.Get(ctx, otherAcceptedKey)
	require.NoError(t, err, "unrelated consumer's accepted downtime window must not be deleted")
	require.Equal(t, int64(193), otherAccepted.WindowStart)
	otherFloor, err := k.DowntimeWindowFloors.Get(ctx, otherPairKey)
	require.NoError(t, err, "unrelated consumer's downtime window floor must not be deleted")
	require.Equal(t, int64(150), otherFloor)
}

// TestCancelConsumerPauseExpiration_RemovesBucketEntryWithoutAffectingOthers
// verifies that cancelling one consumer's scheduled auto-stop removes its id
// from the shared PauseExpirationTimeToConsumerIds time bucket (mirroring
// RemoveConsumerToBeLaunched for the spawn-time queue) without disturbing
// another consumer sharing the same expiration time -- deleting only the
// consumer's own PauseExpirationTime entry would leave its id behind in the
// bucket.
func TestCancelConsumerPauseExpiration_RemovesBucketEntryWithoutAffectingOthers(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cidA := k.FetchAndIncrementConsumerId(ctx)
	cidB := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cidA, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerPhase(ctx, cidB, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cidA, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))
	require.NoError(t, k.PauseConsumerChain(ctx, cidB, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	expirationTime, err := k.GetConsumerPauseExpirationTime(ctx, cidA)
	require.NoError(t, err)
	expirationTimeB, err := k.GetConsumerPauseExpirationTime(ctx, cidB)
	require.NoError(t, err)
	require.Equal(t, expirationTime, expirationTimeB)
	queued, err := k.GetConsumersToBeAutoStopped(ctx, expirationTime)
	require.NoError(t, err)
	require.ElementsMatch(t, []uint64{cidA, cidB}, queued.Ids)

	require.NoError(t, k.CancelConsumerPauseExpiration(ctx, cidA))

	// cidA's per-consumer schedule and bucket entry are both gone.
	_, err = k.GetConsumerPauseExpirationTime(ctx, cidA)
	require.Error(t, err)
	queued, err = k.GetConsumersToBeAutoStopped(ctx, expirationTime)
	require.NoError(t, err)
	require.Equal(t, []uint64{cidB}, queued.Ids)

	// cidB's schedule is untouched.
	stillThere, err := k.GetConsumerPauseExpirationTime(ctx, cidB)
	require.NoError(t, err)
	require.Equal(t, expirationTime, stillThere)
}

// TestCancelConsumerPauseExpiration_NoopWhenNeverPaused verifies that
// cancelling the schedule for a consumer that was never paused (or already
// had its schedule cancelled) does not error.
func TestCancelConsumerPauseExpiration_NoopWhenNeverPaused(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.CancelConsumerPauseExpiration(ctx, cid))
}

// TestResumeConsumerChain_RequiresPaused verifies that only a paused consumer
// can be resumed.
func TestResumeConsumerChain_RequiresPaused(t *testing.T) {
	for _, phase := range []providertypes.ConsumerPhase{
		providertypes.CONSUMER_PHASE_REGISTERED,
		providertypes.CONSUMER_PHASE_INITIALIZED,
		providertypes.CONSUMER_PHASE_LAUNCHED,
		providertypes.CONSUMER_PHASE_STOPPED,
		providertypes.CONSUMER_PHASE_DELETED,
	} {
		t.Run(phase.String(), func(t *testing.T) {
			k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			cid := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerPhase(ctx, cid, phase)

			err := k.ResumeConsumerChain(ctx, cid)
			require.Error(t, err)
			require.ErrorIs(t, err, providertypes.ErrInvalidPhase)
			require.Equal(t, phase, k.GetConsumerPhase(ctx, cid))
		})
	}
}

// TestResumeConsumerChain_RequiresDiscoveredClient verifies the pre-flight
// that runs before the client-status check: a paused consumer with no
// client ever discovered (GetConsumerClientId not found) is rejected with
// ErrInvalidConsumerClient rather than reaching the status check, and no
// state changes (phase stays PAUSED, the auto-stop schedule survives).
func TestResumeConsumerChain_RequiresDiscoveredClient(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	expirationTime, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)

	err = k.ResumeConsumerChain(ctx, cid)
	require.Error(t, err)
	require.ErrorIs(t, err, providertypes.ErrInvalidConsumerClient)

	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	stillThere, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, expirationTime, stillThere)
}

// TestResumeConsumerChain_SendFailureFailsResume verifies that if the forced
// resync snapshot cannot actually be sent (the IBC v2 channel keeper returns
// an error), ResumeConsumerChain returns an error instead of reporting
// success with the snapshot left queued.
//
// The keeper call is exercised inside a cache context that is only written
// back on success, mirroring how cosmos-sdk's message routing wraps every
// message execution: this is what makes the resume tx's rollback guarantee
// real, since the keeper method itself has no rollback of its own. On
// failure the phase and pause-expiration schedule observed on the parent
// context (never written back) must be exactly as they were before the
// call.
func TestResumeConsumerChain_SendFailureFailsResume(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	expirationTime, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active)
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("some transient send error"))

	cacheCtx, writeCache := ctx.CacheContext()
	err = k.ResumeConsumerChain(cacheCtx, cid)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sending resume snapshot")
	// writeCache is deliberately not called: a real message execution
	// discards the cache context on error, so nothing here should be
	// observable on the parent ctx.
	_ = writeCache

	// phase and schedule, observed on the never-written-back parent ctx,
	// are unchanged.
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	stillThere, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, expirationTime, stillThere)
	queued, err := k.GetConsumersToBeAutoStopped(ctx, expirationTime)
	require.NoError(t, err)
	require.Equal(t, []uint64{cid}, queued.Ids)
}

// TestResumeConsumerChain_RejectsInactiveClient verifies the pre-flight
// described in docs/consumer-downtime.md ("The PAUSED phase"): if the
// provider's client of the consumer is not Active, resume fails with
// guidance to bundle ibc-go's MsgRecoverClient into the same governance
// proposal, and no state changes (phase stays PAUSED, the auto-stop
// schedule survives).
func TestResumeConsumerChain_RejectsInactiveClient(t *testing.T) {
	for _, status := range []ibcexported.Status{ibcexported.Expired, ibcexported.Frozen} {
		t.Run(string(status), func(t *testing.T) {
			k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
			defer ctrl.Finish()

			cid := k.FetchAndIncrementConsumerId(ctx)
			k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
			k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
			require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

			mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(status)

			err := k.ResumeConsumerChain(ctx, cid)
			require.Error(t, err)
			require.ErrorIs(t, err, providertypes.ErrConsumerClientNotActive)
			require.Contains(t, err.Error(), "MsgRecoverClient")

			require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
			_, err = k.GetConsumerPauseExpirationTime(ctx, cid)
			require.NoError(t, err)
		})
	}
}

// TestResumeConsumerChain_Success verifies that resuming a paused consumer
// whose client is Active: cancels the auto-stop schedule (per-consumer entry
// and bucket entry), restores phase LAUNCHED, reseeds the liveness clock,
// queues and sends an immediate snapshot VSC packet, and emits the resumed
// event.
func TestResumeConsumerChain_Success(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	expirationTime, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)

	// advance the block time so the reseeded last-ack time is observably
	// different from whatever zero-value or earlier time preceded it
	ctx = ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active)
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)

	require.NoError(t, k.ResumeConsumerChain(ctx, cid))

	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, ctx.BlockTime(), k.GetConsumerLastAckTime(ctx, cid))

	// auto-stop schedule fully cleared, including the bucket entry
	_, err = k.GetConsumerPauseExpirationTime(ctx, cid)
	require.Error(t, err)
	queued, err := k.GetConsumersToBeAutoStopped(ctx, expirationTime)
	require.NoError(t, err)
	require.Empty(t, queued.Ids)

	// the immediate snapshot was sent, not left pending
	require.Empty(t, k.GetPendingVSCPackets(ctx, cid))
	require.Equal(t, uint64(0), k.GetConsumerHighestSentVscId(ctx, cid))

	var found bool
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != "vaas_consumer_resumed" {
			continue
		}
		found = true
		var sawConsumerId bool
		for _, attr := range ev.Attributes {
			if attr.Key == "consumer_id" && attr.Value == "0" {
				sawConsumerId = true
			}
		}
		require.True(t, sawConsumerId, "consumer_id attribute missing on resumed event")
	}
	require.True(t, found, "vaas_consumer_resumed event not emitted")
}

// TestPauseConsumerChainRequiresAReason: a pause without a reason is a
// programming error, refused before any state changes.
func TestPauseConsumerChainRequiresAReason(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.Error(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_UNSPECIFIED))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	_, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.Error(t, err, "no auto-stop may be scheduled for a refused pause")
}

// TestPauseConsumerChainRecordsTheReasonAndNamesItOnTheEvent: the reason is
// kept for the duration of the pause (a resume reads it) and carried on the
// paused event so operators and governance see why the consumer stopped.
func TestPauseConsumerChainRecordsTheReasonAndNamesItOnTheEvent(t *testing.T) {
	k, ctx, ctrl, _ := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)

	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))
	require.Equal(t, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE, k.GetConsumerPauseReason(ctx, cid))
	pausedAt, err := k.GetConsumerPausedAt(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime(), pausedAt)

	var reason string
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type != "vaas_consumer_paused" {
			continue
		}
		for _, attr := range ev.Attributes {
			if attr.Key == "reason" {
				reason = attr.Value
			}
		}
	}
	require.Equal(t, "PAUSE_REASON_DOWNTIME_CHALLENGE", reason, "reason attribute missing on the paused event")
}

// TestPauseConsumerChainRepaysWithheldFeesWhileStillLaunched: every pause,
// not only the one a won challenge triggers, repays the fees withheld from
// accused validators, and does so before the phase flips, since the
// consumer's withdraw lock opens the moment it leaves LAUNCHED. The
// payment callback checks the phase at the moment the transfer happens.
func TestPauseConsumerChainRepaysWithheldFeesWhileStillLaunched(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	val, valBytes := newBondedValidator(t, valAddrCodec, 1)
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	amount := sdk.NewInt64Coin("uphoton", 500)
	putWithheldFeeRecord(t, k, ctx, cid, consAddr, amount, ctx.BlockTime().Add(time.Hour))
	pool := k.GetConsumerFeePoolAddress(cid)

	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), pool, "uphoton").Return(sdk.NewInt64Coin("uphoton", 1000))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), sdk.ConsAddress(consAddr)).Return(val, nil)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: pool.String(), Coins: sdk.NewCoins(amount)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(amount)}},
		).
		DoAndReturn(func(context.Context, banktypes.Input, []banktypes.Output) error {
			require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid),
				"withheld fees must be repaid before the consumer leaves LAUNCHED")
			return nil
		})

	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))

	has, err := k.WithheldFeeRecords.Has(ctx, collections.Join(cid, consAddr))
	require.NoError(t, err)
	require.False(t, has, "paid record must be deleted")
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
}

// resumeMocks sets the expectations a successful ResumeConsumerChain needs
// beyond the client status: the immediate snapshot it builds and sends.
func resumeMocks(mocks testkeeper.MockedKeepers) {
	mocks.MockStakingKeeper.EXPECT().MaxValidators(gomock.Any()).Return(uint32(100), nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().GetBondedValidatorsByPower(gomock.Any()).Return([]stakingtypes.Validator{}, nil).AnyTimes()
	mocks.MockChannelV2Keeper.EXPECT().
		SendPacket(gomock.Any(), gomock.Any()).
		Return(&channeltypesv2.MsgSendPacketResponse{Sequence: 1}, nil).Times(1)
}

// TestResumeAfterALightClientPauseWaitsOutTheTrustingPeriod: the fork's
// evidence stays verifiable, on every submission path, until the pre-fork
// consensus states age out of the client's trusting period, so a resume
// before then would be paused again by the same headers. The resume is
// refused until one trusting period plus the client's clock-drift allowance
// has passed since the pause, and allowed from then on.
func TestResumeAfterALightClientPauseWaitsOutTheTrustingPeriod(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	pausedAt := ctx.BlockTime()
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
	forkKnownAt, err := k.GetConsumerForkKnownAt(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, pausedAt, forkKnownAt, "a light-client pause knows of the fork from the moment it began")
	require.NoError(t, k.SetConsumerClientRecoveredSinceFork(ctx, cid))

	clientState := &ibctmtypes.ClientState{TrustingPeriod: 14 * 24 * time.Hour, MaxClockDrift: 10 * time.Second}
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-0").Return(clientState, true).AnyTimes()
	wait := clientState.TrustingPeriod + clientState.MaxClockDrift

	early := ctx.WithBlockTime(pausedAt.Add(wait - time.Second))
	err = k.ResumeConsumerChain(early, cid)
	require.ErrorIs(t, err, providertypes.ErrForkEvidenceStillVerifiable)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, cid))

	due := ctx.WithBlockTime(pausedAt.Add(wait))
	resumeMocks(mocks)
	require.NoError(t, k.ResumeConsumerChain(due, cid))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, providertypes.PAUSE_REASON_UNSPECIFIED, k.GetConsumerPauseReason(ctx, cid))
	_, err = k.GetConsumerPausedAt(ctx, cid)
	require.Error(t, err, "paused-at must be cleared by the resume")
	_, err = k.GetConsumerForkKnownAt(ctx, cid)
	require.ErrorIs(t, err, collections.ErrNotFound, "the fork time must be cleared by the resume")
	recovered, err := k.IsConsumerClientRecoveredSinceFork(ctx, cid)
	require.NoError(t, err)
	require.False(t, recovered, "the client-recovered mark must be cleared by the resume")
}

// TestResumeAfterADowntimePauseWaitsForNothing: a downtime pause says
// nothing about forks, so its resume reads no client state and imposes no
// wait. No GetClientState expectation is set: gomock fails the test if the
// keeper asks.
func TestResumeAfterADowntimePauseWaitsForNothing(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active)
	resumeMocks(mocks)

	require.NoError(t, k.ResumeConsumerChain(ctx.WithBlockTime(ctx.BlockTime().Add(time.Second)), cid))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, providertypes.PAUSE_REASON_UNSPECIFIED, k.GetConsumerPauseReason(ctx, cid))
}

// TestPauseConsumersWithFrozenClients: the BeginBlock mirror pauses every
// launched consumer whose IBC client is Frozen, under the light-client
// reason, so a fork reported through MsgUpdateClient contains the consumer
// exactly as one reported through MsgSubmitConsumerMisbehaviour does. Active
// and expired clients are left alone, a consumer with no client is skipped,
// and a consumer already paused for a light-client attack whose client is
// still frozen is left as it is.
func TestPauseConsumersWithFrozenClients(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	launched := func(clientId string) uint64 {
		cid := k.FetchAndIncrementConsumerId(ctx)
		k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
		if clientId != "" {
			k.SetConsumerClientId(ctx, cid, clientId)
		}
		return cid
	}
	frozen := launched("07-tendermint-0")
	active := launched("07-tendermint-1")
	expired := launched("07-tendermint-2")
	clientless := launched("")
	pausedForFork := launched("07-tendermint-3")
	require.NoError(t, k.PauseConsumerChain(ctx, pausedForFork, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Frozen)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-1").Return(ibcexported.Active)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-2").Return(ibcexported.Expired)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-3").Return(ibcexported.Frozen)

	pausedForForkAt := ctx.BlockTime()
	ctx = ctx.WithBlockTime(pausedForForkAt.Add(time.Hour))
	k.PauseConsumersWithFrozenClients(ctx)

	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, frozen))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, frozen))
	pausedAt, err := k.GetConsumerPausedAt(ctx, frozen)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime(), pausedAt)
	for _, cid := range []uint64{active, expired, clientless} {
		require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	}
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, pausedForFork))
	forkKnownAt, err := k.GetConsumerForkKnownAt(ctx, pausedForFork)
	require.NoError(t, err)
	require.Equal(t, pausedForForkAt, forkKnownAt, "a client frozen since the pause is the fork the pause already knows of")
}

// TestASecondForkAfterARecoveryDuringTheLightClientPauseMovesTheForkTime: a
// consumer paused for a light-client attack whose client governance recovers
// during the pause, and which is forked again on the recovered client, has
// the fork recorded as known from the block the client is next seen frozen
// at, so the resume's wait counts from the second fork; the pause keeps its
// start and its auto-stop. A recovery alone moves nothing, and a client
// frozen since the pause, with no recovery in between, moves nothing either.
func TestASecondForkAfterARecoveryDuringTheLightClientPauseMovesTheForkTime(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	t0 := ctx.BlockTime()
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
	autoStopAt, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)

	status := ibcexported.Frozen
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").
		DoAndReturn(func(sdk.Context, string) ibcexported.Status { return status }).AnyTimes()
	forkKnownAt := func() time.Time {
		at, err := k.GetConsumerForkKnownAt(ctx, cid)
		require.NoError(t, err)
		return at
	}
	recovered := func() bool {
		r, err := k.IsConsumerClientRecoveredSinceFork(ctx, cid)
		require.NoError(t, err)
		return r
	}

	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(t0.Add(time.Hour)))
	require.Equal(t, t0, forkKnownAt(), "a client frozen since the pause is the fork the pause already knows of")
	require.False(t, recovered())

	status = ibcexported.Active
	t1 := t0.Add(2 * 24 * time.Hour)
	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(t1))
	require.True(t, recovered(), "the recovery is noted")
	require.Equal(t, t0, forkKnownAt(), "a recovery alone moves nothing")

	status = ibcexported.Frozen
	t2 := t1.Add(3 * 24 * time.Hour)
	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(t2))
	require.Equal(t, t2, forkKnownAt(), "the fork is known from the block the client was next seen frozen at")
	require.False(t, recovered(), "the recovery is spent by the fork that followed it")
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, cid))
	pausedAt, err := k.GetConsumerPausedAt(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, t0, pausedAt, "the pause keeps its start")
	stillAutoStopAt, err := k.GetConsumerPauseExpirationTime(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, autoStopAt, stillAutoStopAt, "the auto-stop stands")

	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(t2.Add(time.Hour)))
	require.Equal(t, t2, forkKnownAt(), "still frozen the next block: the second fork is the one known of")
}

// TestPauseConsumersWithFrozenClientsEscalatesAnotherPause: a consumer paused
// for a refusal (or a challenge) whose client freezes during the pause is
// moved under the light-client reason, with the frozen accusations discarded
// as a light-client pause discards them: the pending slash and the epoch
// mark are cancelled, and the withheld fee record the refusal pause froze is
// repaid, its expiry read against the frozen clock although the block time
// is already past it. The fork is recorded as known from the block it was
// seen at, which the resume's trusting-period wait counts from, while the
// paused-at time stays the start of the pause; the auto-stop the earlier
// pause scheduled stands, and the consumer_paused event fires again under the
// new reason. A paused consumer whose client is still active is left as it
// was.
func TestPauseConsumersWithFrozenClientsEscalatesAnotherPause(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	valAddrCodec := address.NewBech32Codec("cosmosvaloper")
	mocks.MockStakingKeeper.EXPECT().ValidatorAddressCodec().Return(valAddrCodec).AnyTimes()
	val, valBytes := newBondedValidator(t, valAddrCodec, 1)
	consAddr, err := val.GetConsAddr()
	require.NoError(t, err)

	pausedAtFirst := ctx.BlockTime()
	refused := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, refused, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, refused, "07-tendermint-0")
	providerAddr := providertypes.NewProviderConsAddress(sdk.ConsAddress([]byte("validator-address-1")))
	putPendingDowntimeSlash(t, k, ctx, refused, providerAddr, math.NewInt(100), ctx.BlockTime().Add(time.Hour), 100)
	k.MarkEpochDowntime(ctx, refused, providerAddr.ToSdkConsAddr())
	withheld := sdk.NewInt64Coin("uphoton", 500)
	putWithheldFeeRecord(t, k, ctx, refused, consAddr, withheld, ctx.BlockTime().Add(time.Hour))
	require.NoError(t, k.PauseConsumerChain(ctx, refused, providertypes.PAUSE_REASON_REFUSAL_THRESHOLD))
	autoStopAt, err := k.GetConsumerPauseExpirationTime(ctx, refused)
	require.NoError(t, err)
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(refused, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.NoError(t, err, "a refusal pause freezes the accusations rather than discarding them")
	has, err := k.WithheldFeeRecords.Has(ctx, collections.Join(refused, consAddr))
	require.NoError(t, err)
	require.True(t, has, "a refusal pause freezes the withheld fee records rather than repaying them")
	_, err = k.GetConsumerForkKnownAt(ctx, refused)
	require.ErrorIs(t, err, collections.ErrNotFound, "a refusal pause records no fork")

	challenged := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, challenged, providertypes.CONSUMER_PHASE_LAUNCHED)
	k.SetConsumerClientId(ctx, challenged, "07-tendermint-1")
	require.NoError(t, k.PauseConsumerChain(ctx, challenged, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE))

	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Frozen)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-1").Return(ibcexported.Active)

	pool := k.GetConsumerFeePoolAddress(refused)
	mocks.MockBankKeeper.EXPECT().GetBalance(gomock.Any(), pool, "uphoton").Return(sdk.NewInt64Coin("uphoton", 1000))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), sdk.ConsAddress(consAddr)).Return(val, nil)
	mocks.MockBankKeeper.EXPECT().
		InputOutputCoins(gomock.Any(),
			banktypes.Input{Address: pool.String(), Coins: sdk.NewCoins(withheld)},
			[]banktypes.Output{{Address: accAddr(valBytes), Coins: sdk.NewCoins(withheld)}},
		).
		Return(nil).Times(1)

	forkSeenAt := pausedAtFirst.Add(3 * 24 * time.Hour)
	ctx = ctx.WithBlockTime(forkSeenAt).WithEventManager(sdk.NewEventManager())
	k.PauseConsumersWithFrozenClients(ctx)

	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, refused))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, refused))
	pausedAt, err := k.GetConsumerPausedAt(ctx, refused)
	require.NoError(t, err)
	require.Equal(t, pausedAtFirst, pausedAt, "the pause still began when the refusal paused the consumer")
	forkKnownAt, err := k.GetConsumerForkKnownAt(ctx, refused)
	require.NoError(t, err)
	require.Equal(t, forkSeenAt, forkKnownAt, "the resume's wait counts from the block the fork was seen")
	_, err = k.PendingDowntimeSlashes.Get(ctx, collections.Join3(refused, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound, "a forked chain's accusations are discarded")
	require.False(t, k.IsEpochDowntime(ctx, refused, providerAddr.ToSdkConsAddr()))
	has, err = k.WithheldFeeRecords.Has(ctx, collections.Join(refused, consAddr))
	require.NoError(t, err)
	require.False(t, has, "the repaid withheld fee record is deleted")
	stillAutoStopAt, err := k.GetConsumerPauseExpirationTime(ctx, refused)
	require.NoError(t, err)
	require.Equal(t, autoStopAt, stillAutoStopAt, "the auto-stop scheduled by the earlier pause stands")

	var pausedEvents []sdk.Event
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == vaastypes.EventTypeConsumerPaused {
			pausedEvents = append(pausedEvents, ev)
		}
	}
	require.Len(t, pausedEvents, 1)
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK.String(), pausedEvents[0].Attributes[2].Value)

	require.Equal(t, providertypes.PAUSE_REASON_DOWNTIME_CHALLENGE, k.GetConsumerPauseReason(ctx, challenged))
	pausedAt, err = k.GetConsumerPausedAt(ctx, challenged)
	require.NoError(t, err)
	require.Equal(t, pausedAtFirst, pausedAt)
	_, err = k.GetConsumerForkKnownAt(ctx, challenged)
	require.ErrorIs(t, err, collections.ErrNotFound)
}

// TestResumeAfterAnEscalatedPauseWaitsFromTheFreeze: a consumer paused for a
// refusal and moved under the light-client reason when its client froze days
// later waits one trusting period (plus the clock-drift allowance) from the
// freeze, not from the start of the pause: the pre-fork consensus states the
// conflicting headers verify against stay in the client that long after the
// fork is known. A resume due by the start of the pause but not by the
// freeze is refused.
func TestResumeAfterAnEscalatedPauseWaitsFromTheFreeze(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	k.SetInfractionParams(ctx, providertypes.DefaultInfractionParameters())

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	pausedAt := ctx.BlockTime()
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_REFUSAL_THRESHOLD))

	frozenAt := pausedAt.Add(3 * 24 * time.Hour)
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Frozen).Times(1)
	k.PauseConsumersWithFrozenClients(ctx.WithBlockTime(frozenAt))
	require.Equal(t, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK, k.GetConsumerPauseReason(ctx, cid))

	clientState := &ibctmtypes.ClientState{TrustingPeriod: 14 * 24 * time.Hour, MaxClockDrift: 10 * time.Second}
	mocks.MockClientKeeper.EXPECT().GetClientStatus(gomock.Any(), "07-tendermint-0").Return(ibcexported.Active).AnyTimes()
	mocks.MockClientKeeper.EXPECT().GetClientState(gomock.Any(), "07-tendermint-0").Return(clientState, true).AnyTimes()
	wait := clientState.TrustingPeriod + clientState.MaxClockDrift

	early := ctx.WithBlockTime(frozenAt.Add(wait - time.Second))
	require.True(t, early.BlockTime().After(pausedAt.Add(wait)), "the early resume is past the wait counted from the pause")
	err := k.ResumeConsumerChain(early, cid)
	require.ErrorIs(t, err, providertypes.ErrForkEvidenceStillVerifiable)
	require.Equal(t, providertypes.CONSUMER_PHASE_PAUSED, k.GetConsumerPhase(ctx, cid))

	due := ctx.WithBlockTime(frozenAt.Add(wait))
	resumeMocks(mocks)
	require.NoError(t, k.ResumeConsumerChain(due, cid))
	require.Equal(t, providertypes.CONSUMER_PHASE_LAUNCHED, k.GetConsumerPhase(ctx, cid))
	_, err = k.GetConsumerForkKnownAt(ctx, cid)
	require.ErrorIs(t, err, collections.ErrNotFound, "the fork time must be cleared by the resume")
}

// TestStopClearsThePauseReason: a consumer stopped out of PAUSED (the
// auto-stop or a governance removal) is no longer paused for any reason.
func TestStopClearsThePauseReason(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_LAUNCHED)
	require.NoError(t, k.PauseConsumerChain(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
	require.NoError(t, k.SetConsumerClientRecoveredSinceFork(ctx, cid))

	require.NoError(t, k.StopAndPrepareForConsumerRemoval(ctx, cid))
	require.Equal(t, providertypes.PAUSE_REASON_UNSPECIFIED, k.GetConsumerPauseReason(ctx, cid))
	_, err := k.GetConsumerPausedAt(ctx, cid)
	require.Error(t, err, "paused-at must be cleared by the stop")
	_, err = k.GetConsumerForkKnownAt(ctx, cid)
	require.ErrorIs(t, err, collections.ErrNotFound, "the fork time must be cleared by the stop")
	recovered, err := k.IsConsumerClientRecoveredSinceFork(ctx, cid)
	require.NoError(t, err)
	require.False(t, recovered, "the client-recovered mark must be cleared by the stop")
}

// TestDeleteConsumerChainClearsPauseReasonAndPausedAt: deletion is the
// consumer's terminal erasure and leaves none of the pause records behind.
func TestDeleteConsumerChainClearsPauseReasonAndPausedAt(t *testing.T) {
	k, ctx, ctrl, mocks := testkeeper.GetProviderKeeperAndCtx(t, testkeeper.NewInMemKeeperParams(t))
	defer ctrl.Finish()

	cid := k.FetchAndIncrementConsumerId(ctx)
	k.SetConsumerPhase(ctx, cid, providertypes.CONSUMER_PHASE_STOPPED)
	k.SetConsumerClientId(ctx, cid, "07-tendermint-0")
	poolAddr := k.GetConsumerFeePoolAddress(cid)
	require.NoError(t, k.FeePoolAddressToConsumerId.Set(ctx, poolAddr, cid))
	mocks.MockBankKeeper.EXPECT().GetAllBalances(ctx, poolAddr).Return(sdk.NewCoins())

	require.NoError(t, k.SetConsumerPauseReason(ctx, cid, providertypes.PAUSE_REASON_LIGHT_CLIENT_ATTACK))
	require.NoError(t, k.SetConsumerPausedAt(ctx, cid, ctx.BlockTime()))
	require.NoError(t, k.SetConsumerForkKnownAt(ctx, cid, ctx.BlockTime()))
	require.NoError(t, k.SetConsumerClientRecoveredSinceFork(ctx, cid))

	require.NoError(t, k.DeleteConsumerChain(ctx, cid))

	require.Equal(t, providertypes.PAUSE_REASON_UNSPECIFIED, k.GetConsumerPauseReason(ctx, cid))
	_, err := k.GetConsumerPausedAt(ctx, cid)
	require.Error(t, err)
	_, err = k.GetConsumerForkKnownAt(ctx, cid)
	require.ErrorIs(t, err, collections.ErrNotFound)
	recovered, err := k.IsConsumerClientRecoveredSinceFork(ctx, cid)
	require.NoError(t, err)
	require.False(t, recovered)
}
