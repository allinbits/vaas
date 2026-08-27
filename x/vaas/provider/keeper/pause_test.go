package keeper_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
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

			err := k.PauseConsumerChain(ctx, cid)
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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))
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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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

// TestStopAndPrepareForConsumerRemoval_CancelsDowntimeState verifies that
// stopping a consumer (whether from the liveness sweep or elsewhere) cancels
// any pending downtime state, while the accepted-window records and the
// pruned floor deliberately survive: a cancelled window must not become
// re-submittable, so only DeleteConsumerChain erases them.
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

	require.NoError(t, k.StopAndPrepareForConsumerRemoval(ctx, cid))

	_, err := k.PendingDowntimeSlashes.Get(ctx, collections.Join3(cid, providerAddr.ToSdkConsAddr().Bytes(), int64(100)))
	require.ErrorIs(t, err, collections.ErrNotFound)
	require.False(t, k.IsEpochDowntime(ctx, cid, providerAddr.ToSdkConsAddr()))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cidA))
	require.NoError(t, k.PauseConsumerChain(ctx, cidB))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
			require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
	require.NoError(t, k.PauseConsumerChain(ctx, cid))

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
