package keeper_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	testkeeper "github.com/allinbits/vaas/testutil/keeper"
	providerkeeper "github.com/allinbits/vaas/x/vaas/provider/keeper"
	types "github.com/allinbits/vaas/x/vaas/provider/types"
)

// setupEquivocationQueueTest builds a keeper with double-sign infraction
// params and a bonded validator.
func setupEquivocationQueueTest(t *testing.T) (
	providerkeeper.Keeper, sdk.Context, *gomock.Controller, testkeeper.MockedKeepers, stakingtypes.Validator, types.ProviderConsAddress,
) {
	t.Helper()
	infractionParams := types.InfractionParameters{
		DoubleSign: &types.SlashJailParameters{
			JailDuration:  time.Duration(1<<63 - 1),
			SlashFraction: math.LegacyNewDecWithPrec(5, 2), // 5%
			Tombstone:     true,
		},
		Downtime: &types.SlashJailParameters{
			SlashFraction: math.LegacyNewDecWithPrec(1, 4),
		},
	}
	k, ctx, ctrl, mocks, validator, providerAddr := setupSweepTest(t, infractionParams)
	// 500 tokens at a power reduction of 1: the power every execution
	// expectation below sizes the slash from.
	validator.Tokens = math.NewInt(500)
	validator.DelegatorShares = math.LegacyNewDec(500)
	return k, ctx, ctrl, mocks, validator, providerAddr
}

// expectQueueJail mocks the queue-time jail: validator lookup, tombstone
// check, jail, and a jail horizon of the execution time plus the 24h margin.
func expectQueueJail(k providerkeeper.Keeper, mocks testkeeper.MockedKeepers, ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress) {
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false)
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay).Add(24*time.Hour))
}

// expectNoExistingUnbondings mocks empty unbonding/redelegation sets for the
// hold placement at queue time.
func expectNoExistingUnbondings(t *testing.T, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers, ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress) {
	t.Helper()
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valAddr).Return(nil, nil)
}

// TestQueuePendingEquivocationJailsWithoutPunishing pins the queue-time
// behavior: the validator is jailed, existing unbonding ops are held, the
// entry is stored, and no slash or tombstone happens (no expectations for
// them are set, so the controller proves it). Re-queueing the same evidence
// is a no-op.
func TestQueuePendingEquivocationJailsWithoutPunishing(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	const consumerID = uint64(0)
	const infractionHeight = int64(42)

	// One in-flight undelegation with two entries gets held at queue time.
	// The validator is resolved to key the entry, then again by the jail;
	// each re-submission below resolves it once more before finding the
	// entry and reads the tombstone flag to report it.
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil).Times(4)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false).Times(2)
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay).Add(24*time.Hour))
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valAddr).Return([]stakingtypes.UnbondingDelegation{
		{Entries: []stakingtypes.UnbondingDelegationEntry{{UnbondingId: 7}, {UnbondingId: 9}}},
	}, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(7)).Return(nil)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(9)).Return(nil)

	alreadyTombstoned, err := k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, infractionHeight)
	require.NoError(t, err)
	require.False(t, alreadyTombstoned)

	key := collections.Join3(consumerID, consAddr.Bytes(), infractionHeight)
	entry, err := k.PendingEquivocationPunishments.Get(ctx, key)
	require.NoError(t, err)
	require.Equal(t, ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay), entry.ExecutesAt)
	require.False(t, entry.ExecutesAtExtended)

	held, err := k.HeldUnbondingOps.Has(ctx, collections.Join(consAddr.Bytes(), uint64(7)))
	require.NoError(t, err)
	require.True(t, held)

	// Same evidence again: beyond resolving the validator to find the entry
	// and reading its tombstone flag, nothing runs (no new expectations) and
	// nothing changes.
	alreadyTombstoned, err = k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, infractionHeight)
	require.NoError(t, err)
	require.False(t, alreadyTombstoned)

	// A tombstone that landed meanwhile (provider-native evidence) is
	// reported on the next re-submission, so the caller logs the truth.
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(true)
	alreadyTombstoned, err = k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, infractionHeight)
	require.NoError(t, err)
	require.True(t, alreadyTombstoned)
}

// TestQueuePendingEquivocationTombstonedIsIdempotent proves re-submitted
// evidence for an already-tombstoned validator reports alreadyTombstoned and
// queues nothing.
func TestQueuePendingEquivocationTombstonedIsIdempotent(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(true)

	alreadyTombstoned, err := k.QueuePendingEquivocationPunishment(ctx, 0, providerAddr, 42)
	require.NoError(t, err)
	require.True(t, alreadyTombstoned)

	has, err := k.PendingEquivocationPunishments.Has(ctx, collections.Join3(uint64(0), consAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.False(t, has)
}

// TestSweepExecutesMaturedEquivocation proves the execution path: at maturity
// with no removal vote, the validator is slashed and tombstoned at the
// double-sign parameters, the entry is removed, and the holds are released.
func TestSweepExecutesMaturedEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	const consumerID = uint64(0)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	// Queue with no existing unbondings.
	expectQueueJail(k, mocks, ctx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, ctx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, 42)
	require.NoError(t, err)

	// Record one hook-held op to observe the release.
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(11)).Return(nil)
	require.NoError(t, k.HoldUnbondingOpForTest(ctx, consAddr, 11))

	// Advance past maturity; no removal vote.
	lateCtx := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))

	// Execution: slash (with the unbonding/redelegation scans SlashValidator
	// performs) and jail+tombstone.
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), consAddr).Return(false).Times(2)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1))
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		gomock.Any(), consAddr, int64(0), int64(500), gomock.Any(), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
	).Return(math.NewInt(25), nil)
	mocks.MockStakingKeeper.EXPECT().Jail(gomock.Any(), consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(gomock.Any(), consAddr, gomock.Any())
	mocks.MockSlashingKeeper.EXPECT().Tombstone(gomock.Any(), consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(lateCtx, uint64(11)).Return(nil)

	k.SweepPendingEquivocationPunishments(lateCtx)

	has, err := k.PendingEquivocationPunishments.Has(lateCtx, collections.Join3(consumerID, consAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.False(t, has)
	heldStill, err := k.HeldUnbondingOps.Has(lateCtx, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, heldStill)
}

// TestSweepDefersEquivocationBehindRemovalVote proves the one-time extension
// past a live removal vote and the paused-consumer freeze.
func TestSweepDefersEquivocationBehindRemovalVote(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	const consumerID = uint64(0)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	expectQueueJail(k, mocks, ctx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, ctx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, 42)
	require.NoError(t, err)

	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	voteEnd := matured.BlockTime().Add(time.Hour)
	k.OverrideRemovalVoteForTest(func(_ sdk.Context, cid uint64) (uint64, time.Time, bool) {
		require.Equal(t, consumerID, cid)
		return 7, voteEnd, true
	})

	// Deferral extends the jail horizon too.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(matured, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(matured, consAddr, gomock.Any())

	k.SweepPendingEquivocationPunishments(matured)

	key := collections.Join3(consumerID, consAddr.Bytes(), int64(42))
	entry, err := k.PendingEquivocationPunishments.Get(matured, key)
	require.NoError(t, err)
	require.True(t, entry.ExecutesAtExtended)
	require.Equal(t, uint64(7), entry.DeferredByProposalId, "the deferral must record the vote it waits on")
	require.Equal(t, voteEnd.Add(k.GetParams(ctx).RemovalVoteDeferralMargin), entry.ExecutesAt)

	// A paused consumer freezes its entries: no execution, no extension, and
	// nothing else runs (no expectations are set).
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_PAUSED)
	frozen := matured.WithBlockTime(entry.ExecutesAt.Add(time.Hour))
	k.SweepPendingEquivocationPunishments(frozen)
	_, err = k.PendingEquivocationPunishments.Get(frozen, key)
	require.NoError(t, err, "a paused consumer's pending punishment must stay put")
}

// expectExecution mocks the slash + tombstone path executePendingEquivocation
// runs for a bonded validator with no unbondings. Execution runs inside a
// cache context, so the calls are matched on any context.
func expectExecution(t *testing.T, k providerkeeper.Keeper, mocks testkeeper.MockedKeepers, ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress) {
	t.Helper()
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), consAddr).Return(false).Times(2)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1))
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		gomock.Any(), consAddr, int64(0), int64(500), math.LegacyNewDecWithPrec(5, 2), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
	).Return(math.NewInt(25), nil)
	mocks.MockStakingKeeper.EXPECT().Jail(gomock.Any(), consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(gomock.Any(), consAddr, gomock.Any())
	mocks.MockSlashingKeeper.EXPECT().Tombstone(gomock.Any(), consAddr)
}

// queueWithHeldOp queues a punishment for a launched consumer and records one
// hook-held unbonding op, returning the entry key.
func queueWithHeldOp(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, validator stakingtypes.Validator, providerAddr types.ProviderConsAddress, consumerID uint64, opID uint64) collections.Triple[uint64, []byte, int64] {
	t.Helper()
	consAddr := providerAddr.ToSdkConsAddr()
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)
	expectQueueJail(k, mocks, ctx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, ctx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, 42)
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, opID).Return(nil)
	require.NoError(t, k.HoldUnbondingOpForTest(ctx, consAddr, opID))
	return collections.Join3(consumerID, consAddr.Bytes(), int64(42))
}

// TestGovernanceRemovalCancelsPendingEquivocation proves the one cancelling
// verdict: the governance RemoveConsumer path cancels the consumer's pending
// punishments, releases the holds, and opens the jail, while
// StopAndPrepareForConsumerRemoval on its own does none of that.
func TestGovernanceRemovalCancelsPendingEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	const consumerID = uint64(0)
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, consumerID, 13)

	// The stop primitive every stop path shares leaves the punishment alone.
	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	require.NoError(t, k.StopAndPrepareForConsumerRemoval(ctx, consumerID))
	has, err := k.PendingEquivocationPunishments.Has(ctx, key)
	require.NoError(t, err)
	require.True(t, has, "a stop by itself must not cancel a pending punishment")

	// The governance verdict does.
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(ctx, uint64(13)).Return(nil)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime())
	k.CancelPendingEquivocationPunishmentsForConsumer(ctx, consumerID)

	has, err = k.PendingEquivocationPunishments.Has(ctx, key)
	require.NoError(t, err)
	require.False(t, has)
	heldStill, err := k.HeldUnbondingOps.Has(ctx, collections.Join(consAddr.Bytes(), uint64(13)))
	require.NoError(t, err)
	require.False(t, heldStill)
}

// TestRemoveConsumerHandlerCancelsPendingEquivocation drives the governance
// path through the message handler: a successful MsgRemoveConsumer cancels
// the consumer's pending punishment.
func TestRemoveConsumerHandlerCancelsPendingEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	const consumerID = uint64(0)
	k.SetConsumerChainId(ctx, consumerID, "consumer-chain")
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, consumerID, 17)

	mocks.MockStakingKeeper.EXPECT().UnbondingTime(gomock.Any()).Return(21*24*time.Hour, nil).AnyTimes()
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(ctx, uint64(17)).Return(nil)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime())

	msgServer := providerkeeper.NewMsgServerImpl(&k)
	_, err := msgServer.RemoveConsumer(ctx, &types.MsgRemoveConsumer{
		Authority:  k.GetAuthority(),
		ConsumerId: consumerID,
	})
	require.NoError(t, err)
	require.Equal(t, types.CONSUMER_PHASE_STOPPED, k.GetConsumerPhase(ctx, consumerID))

	has, err := k.PendingEquivocationPunishments.Has(ctx, key)
	require.NoError(t, err)
	require.False(t, has, "a governance removal must cancel the pending punishment")
}

// TestNonGovernanceStopDoesNotCancelPendingEquivocation proves that a
// consumer stopped without a governance verdict (liveness sweep, lapsed
// pause) keeps its pending punishments running: at maturity the sweep
// executes the slash and tombstone and releases the holds.
func TestNonGovernanceStopDoesNotCancelPendingEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	const consumerID = uint64(0)
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, consumerID, 19)

	// The chain dies of liveness (or its pause lapses): STOPPED without a
	// governance verdict.
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_STOPPED)

	// Not matured yet: nothing happens, in particular no cancellation.
	k.SweepPendingEquivocationPunishments(ctx)
	has, err := k.PendingEquivocationPunishments.Has(ctx, key)
	require.NoError(t, err)
	require.True(t, has, "a non-governance stop must not cancel the pending punishment")

	// Matured: it executes.
	lateCtx := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	expectExecution(t, k, mocks, lateCtx, validator, consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(lateCtx, uint64(19)).Return(nil)
	k.SweepPendingEquivocationPunishments(lateCtx)

	has, err = k.PendingEquivocationPunishments.Has(lateCtx, key)
	require.NoError(t, err)
	require.False(t, has, "the punishment must execute once matured")
}

// deferEquivocationBehindVote queues a punishment with one held op, lets it
// mature while a removal vote for the consumer (proposal 7) is live, and
// returns the entry key, the deferred entry, and the matured context.
func deferEquivocationBehindVote(t *testing.T, k providerkeeper.Keeper, ctx sdk.Context, mocks testkeeper.MockedKeepers, validator stakingtypes.Validator, providerAddr types.ProviderConsAddress, consumerID, opID uint64) (collections.Triple[uint64, []byte, int64], types.PendingEquivocationPunishment, sdk.Context) {
	t.Helper()
	consAddr := providerAddr.ToSdkConsAddr()
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, consumerID, opID)

	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	voteEnd := matured.BlockTime().Add(time.Hour)
	k.OverrideRemovalVoteForTest(func(_ sdk.Context, _ uint64) (uint64, time.Time, bool) {
		return 7, voteEnd, true
	})
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(matured, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(matured, consAddr, voteEnd.Add(k.GetParams(ctx).RemovalVoteDeferralMargin).Add(24*time.Hour))
	k.SweepPendingEquivocationPunishments(matured)
	entry, err := k.PendingEquivocationPunishments.Get(matured, key)
	require.NoError(t, err)
	require.Equal(t, uint64(7), entry.DeferredByProposalId)

	// The scan is not consulted again for an extended entry; only the
	// recorded proposal's status is.
	k.OverrideRemovalVoteForTest(func(_ sdk.Context, _ uint64) (uint64, time.Time, bool) {
		return 0, time.Time{}, false
	})
	return key, entry, matured
}

// expectCancellation mocks the hold release and jail opening a cancellation
// performs for a validator's last pending entry.
func expectCancellation(mocks testkeeper.MockedKeepers, ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress, opID uint64) {
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(ctx, opID).Return(nil)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, ctx.BlockTime())
}

// TestPassedRemovalVoteCancelsDeferredEquivocation: the vote the entry
// deferred behind passed, so the punishment is cancelled at its extended
// maturity, holds released and jail opened.
func TestPassedRemovalVoteCancelsDeferredEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	key, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, 0, 23)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, proposalId uint64) (govv1.ProposalStatus, time.Time, bool) {
		require.Equal(t, uint64(7), proposalId)
		return govv1.StatusPassed, time.Time{}, true
	})

	decided := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	expectCancellation(mocks, decided, validator, consAddr, 23)
	k.SweepPendingEquivocationPunishments(decided)

	has, err := k.PendingEquivocationPunishments.Has(decided, key)
	require.NoError(t, err)
	require.False(t, has, "a passed removal vote must cancel the punishment it deferred")
	heldStill, err := k.HeldUnbondingOps.Has(decided, collections.Join(consAddr.Bytes(), uint64(23)))
	require.NoError(t, err)
	require.False(t, heldStill)
}

// TestFailedRemovalOfStoppedConsumerCancelsDeferredEquivocation is the
// scenario the recorded proposal id exists for: the chain dies of liveness
// during the vote, the vote passes, and gov marks the proposal FAILED because
// its MsgRemoveConsumer cannot run against a stopped consumer. The tally is
// the verdict, so the punishment is cancelled.
func TestFailedRemovalOfStoppedConsumerCancelsDeferredEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	const consumerID = uint64(0)
	key, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, consumerID, 23)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_STOPPED)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, _ uint64) (govv1.ProposalStatus, time.Time, bool) {
		return govv1.StatusFailed, time.Time{}, true
	})

	decided := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	expectCancellation(mocks, decided, validator, consAddr, 23)
	k.SweepPendingEquivocationPunishments(decided)

	has, err := k.PendingEquivocationPunishments.Has(decided, key)
	require.NoError(t, err)
	require.False(t, has, "a passed vote whose removal found the consumer already stopped must cancel the punishment")
}

// TestFailedRemovalOfRunningConsumerExecutesDeferredEquivocation: FAILED
// with the consumer still running means the removal did not happen for some
// other reason (another message in the proposal failed), so the verdict on
// the chain never landed and the punishment executes.
func TestFailedRemovalOfRunningConsumerExecutesDeferredEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	key, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, 0, 29)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, _ uint64) (govv1.ProposalStatus, time.Time, bool) {
		return govv1.StatusFailed, time.Time{}, true
	})

	decided := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	expectExecution(t, k, mocks, decided, validator, consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(decided, uint64(29)).Return(nil)
	k.SweepPendingEquivocationPunishments(decided)

	has, err := k.PendingEquivocationPunishments.Has(decided, key)
	require.NoError(t, err)
	require.False(t, has, "a failed removal of a running consumer must let the punishment execute")
}

// TestRejectedRemovalVoteExecutesDeferredEquivocation is the other verdict:
// the vote the entry deferred behind did not pass, so the punishment executes
// at its extended maturity, stopped consumer or not.
func TestRejectedRemovalVoteExecutesDeferredEquivocation(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	const consumerID = uint64(0)
	key, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, consumerID, 29)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_STOPPED)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, _ uint64) (govv1.ProposalStatus, time.Time, bool) {
		return govv1.StatusRejected, time.Time{}, true
	})

	decided := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	expectExecution(t, k, mocks, decided, validator, consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(decided, uint64(29)).Return(nil)
	k.SweepPendingEquivocationPunishments(decided)

	has, err := k.PendingEquivocationPunishments.Has(decided, key)
	require.NoError(t, err)
	require.False(t, has, "a rejected removal vote must let the deferred punishment execute")
}

// TestOpenRemovalVoteAtExtendedMaturityKeepsDeferring: the proposal the entry
// deferred behind is still in its voting period when the extended maturity
// arrives (gov moved the end after a late quorum), so the entry waits for its
// tally: the maturity follows the new voting end and the jail horizon with
// it. A different proposal would not have this effect: the entry only ever
// waits for the one it recorded.
func TestOpenRemovalVoteAtExtendedMaturityKeepsDeferring(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	key, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, 0, 31)

	extendedEnd := entry.ExecutesAt.Add(2 * time.Hour)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, proposalId uint64) (govv1.ProposalStatus, time.Time, bool) {
		require.Equal(t, uint64(7), proposalId)
		return govv1.StatusVotingPeriod, extendedEnd, true
	})

	margin := k.GetParams(ctx).RemovalVoteDeferralMargin
	stillVoting := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(stillVoting, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(stillVoting, consAddr, extendedEnd.Add(margin).Add(24*time.Hour))
	k.SweepPendingEquivocationPunishments(stillVoting)

	again, err := k.PendingEquivocationPunishments.Get(stillVoting, key)
	require.NoError(t, err, "the entry must still be pending")
	require.Equal(t, extendedEnd.Add(margin), again.ExecutesAt)
	require.Equal(t, uint64(7), again.DeferredByProposalId)
	require.True(t, again.ExecutesAtExtended)
}

// TestExecutionSlashesBondedStakeOfJailedValidator pins the sizing of the
// deferred slash: the queue-time jail took the validator out of the power
// index, so the slash must be sized from its tokens (plus the stake tied up in
// held undelegations), at the double-sign fraction, or the bonded stake would
// escape the punishment the holds exist to preserve.
func TestExecutionSlashesBondedStakeOfJailedValidator(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	const consumerID = uint64(0)
	k.SetConsumerPhase(ctx, consumerID, types.CONSUMER_PHASE_LAUNCHED)

	expectQueueJail(k, mocks, ctx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, ctx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(ctx, consumerID, providerAddr, 42)
	require.NoError(t, err)

	// By execution time the validator is jailed and unbonding, worth 1000
	// tokens, with a held undelegation of 200 in flight.
	jailed := validator
	jailed.Jailed = true
	jailed.Status = stakingtypes.Unbonding
	jailed.Tokens = math.NewInt(1000)
	undelegation := stakingtypes.UnbondingDelegation{Entries: []stakingtypes.UnbondingDelegationEntry{{
		InitialBalance: math.NewInt(200), Balance: math.NewInt(200), UnbondingId: 7, UnbondingOnHoldRefCount: 1,
	}}}

	lateCtx := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(jailed, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), consAddr).Return(false).Times(2)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), valAddr).Return([]stakingtypes.UnbondingDelegation{undelegation}, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1))
	// ComputePowerToSlash measures the undelegation with a full slash in a
	// cached context; the real slash then happens once, sized 1000 + 200.
	mocks.MockStakingKeeper.EXPECT().SlashUnbondingDelegation(gomock.Any(), undelegation, int64(0), math.LegacyOneDec()).Return(math.NewInt(200), nil)
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(
		gomock.Any(), consAddr, int64(0), int64(1200), math.LegacyNewDecWithPrec(5, 2), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN,
	).Return(math.NewInt(60), nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(gomock.Any(), consAddr, gomock.Any())
	mocks.MockSlashingKeeper.EXPECT().Tombstone(gomock.Any(), consAddr)

	k.SweepPendingEquivocationPunishments(lateCtx)

	has, err := k.PendingEquivocationPunishments.Has(lateCtx, collections.Join3(consumerID, consAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.False(t, has, "the punishment executed")
}

// TestQueueToleratesCompletedValidatorUnbondingIds: x/staking never clears a
// validator's UnbondingIds once its own unbonding completes, so a validator
// that once left and rejoined the set carries an id whose index is gone.
// Holding it fails with ErrNoValidatorFound, which must read as "already
// completed, nothing to hold" rather than reject the evidence for good.
func TestQueueToleratesCompletedValidatorUnbondingIds(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()

	consAddr := providerAddr.ToSdkConsAddr()
	validator.UnbondingIds = []uint64{3, 8}
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, consAddr).Return(false)
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, gomock.Any())
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(3)).Return(stakingtypes.ErrNoValidatorFound)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(8)).Return(nil)

	_, err = k.QueuePendingEquivocationPunishment(ctx, 0, providerAddr, 42)
	require.NoError(t, err, "a completed unbonding id must not block the evidence")

	stale, err := k.HeldUnbondingOps.Has(ctx, collections.Join(consAddr.Bytes(), uint64(3)))
	require.NoError(t, err)
	require.False(t, stale, "nothing is recorded for an operation that already completed")
	live, err := k.HeldUnbondingOps.Has(ctx, collections.Join(consAddr.Bytes(), uint64(8)))
	require.NoError(t, err)
	require.True(t, live)
}

// TestQueueKeysPunishmentByTheLiveAddressAndHooksHoldThere: evidence naming a
// consensus key the validator rotated away from is queued under the address
// it runs now, which is also the address the unbonding hook resolves a new
// operation to, so the operation is held.
func TestQueueKeysPunishmentByTheLiveAddressAndHooksHoldThere(t *testing.T) {
	k, ctx, ctrl, mocks, _, oldProviderAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	k.SetConsumerPhase(ctx, 0, types.CONSUMER_PHASE_LAUNCHED)

	oldAddr := oldProviderAddr.ToSdkConsAddr()
	rotated, newProviderAddr := newSweepValidator(t)
	rotated.Tokens = math.NewInt(500)
	newAddr := newProviderAddr.ToSdkConsAddr()
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(rotated.GetOperator())
	require.NoError(t, err)

	// x/staking answers for the rotated-away address through the old-to-new
	// mapping and for the live one directly, with the same validator.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, oldAddr).Return(rotated, nil)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, newAddr).Return(rotated, nil)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(ctx, newAddr).Return(false)
	mocks.MockStakingKeeper.EXPECT().Jail(ctx, newAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, newAddr, gomock.Any())
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(ctx, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(ctx, valAddr).Return(nil, nil)

	_, err = k.QueuePendingEquivocationPunishment(ctx, 0, oldProviderAddr, 42)
	require.NoError(t, err)

	underOld, err := k.PendingEquivocationPunishments.Has(ctx, collections.Join3(uint64(0), oldAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.False(t, underOld)
	entry, err := k.PendingEquivocationPunishments.Get(ctx, collections.Join3(uint64(0), newAddr.Bytes(), int64(42)))
	require.NoError(t, err, "the entry is keyed by the live address")
	require.Equal(t, newAddr.Bytes(), entry.ProviderConsAddr)

	// A delegator undelegates from the accused validator: the hook resolves
	// the operation to the live address and holds it.
	ubd := stakingtypes.UnbondingDelegation{ValidatorAddress: rotated.GetOperator()}
	mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(77)).Return(stakingtypes.UnbondingType_UnbondingDelegation, nil)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationByUnbondingID(ctx, uint64(77)).Return(ubd, nil)
	mocks.MockStakingKeeper.EXPECT().GetValidator(ctx, sdk.ValAddress(valAddr)).Return(rotated, nil)
	mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(77)).Return(nil)
	require.NoError(t, k.Hooks().AfterUnbondingInitiated(ctx, 77))

	held, err := k.HeldUnbondingOps.Has(ctx, collections.Join(newAddr.Bytes(), uint64(77)))
	require.NoError(t, err)
	require.True(t, held, "the hook must find the entry under the live address")
}

// TestRotationAfterQueueMovesPunishmentAndHolds: a consensus-key rotation
// after the queue re-keys the pending punishment and its holds to the new
// address, so the governance cancellation later finds and releases them
// there.
func TestRotationAfterQueueMovesPunishmentAndHolds(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	oldAddr := providerAddr.ToSdkConsAddr()

	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)

	rotated, newProviderAddr := newSweepValidator(t)
	newAddr := newProviderAddr.ToSdkConsAddr()
	k.MigrateStateOnConsPubKeyRotation(ctx, providerAddr, newProviderAddr)

	stillOld, err := k.PendingEquivocationPunishments.Has(ctx, key)
	require.NoError(t, err)
	require.False(t, stillOld, "nothing stays under the old address")
	moved, err := k.PendingEquivocationPunishments.Get(ctx, collections.Join3(uint64(0), newAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.Equal(t, newAddr.Bytes(), moved.ProviderConsAddr)
	oldHold, err := k.HeldUnbondingOps.Has(ctx, collections.Join(oldAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, oldHold)
	newHold, err := k.HeldUnbondingOps.Has(ctx, collections.Join(newAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.True(t, newHold)

	// Governance removes the consumer: the cancellation releases the moved
	// hold and opens the jail under the live address.
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(ctx, uint64(11)).Return(nil)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, newAddr).Return(rotated, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, newAddr, ctx.BlockTime())
	k.CancelPendingEquivocationPunishmentsForConsumer(ctx, 0)
	newHold, err = k.HeldUnbondingOps.Has(ctx, collections.Join(newAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, newHold)
}

// TestAfterUnbondingInitiatedHoldsOnlyAccusedValidatorsOps covers the three
// kinds of unbonding operation the hook resolves (an undelegation, a
// redelegation out, the validator's own unbonding): each is held when its
// validator has a pending punishment, and an operation of any other validator
// passes through untouched.
func TestAfterUnbondingInitiatedHoldsOnlyAccusedValidatorsOps(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)

	other, _ := newSweepValidator(t)
	otherValAddr, err := k.ValidatorAddressCodec().StringToBytes(other.GetOperator())
	require.NoError(t, err)

	cases := []struct {
		name   string
		id     uint64
		expect func()
		held   bool
	}{
		{"undelegation from the accused", 21, func() {
			mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(21)).Return(stakingtypes.UnbondingType_UnbondingDelegation, nil)
			mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationByUnbondingID(ctx, uint64(21)).Return(stakingtypes.UnbondingDelegation{ValidatorAddress: validator.GetOperator()}, nil)
			mocks.MockStakingKeeper.EXPECT().GetValidator(ctx, sdk.ValAddress(valAddr)).Return(validator, nil)
			mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(21)).Return(nil)
		}, true},
		{"redelegation away from the accused", 22, func() {
			mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(22)).Return(stakingtypes.UnbondingType_Redelegation, nil)
			mocks.MockStakingKeeper.EXPECT().GetRedelegationByUnbondingID(ctx, uint64(22)).Return(stakingtypes.Redelegation{ValidatorSrcAddress: validator.GetOperator()}, nil)
			mocks.MockStakingKeeper.EXPECT().GetValidator(ctx, sdk.ValAddress(valAddr)).Return(validator, nil)
			mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(22)).Return(nil)
		}, true},
		{"the accused validator's own unbonding", 23, func() {
			mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(23)).Return(stakingtypes.UnbondingType_ValidatorUnbonding, nil)
			mocks.MockStakingKeeper.EXPECT().GetValidatorByUnbondingID(ctx, uint64(23)).Return(validator, nil)
			mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, uint64(23)).Return(nil)
		}, true},
		{"undelegation from another validator", 24, func() {
			mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(24)).Return(stakingtypes.UnbondingType_UnbondingDelegation, nil)
			mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationByUnbondingID(ctx, uint64(24)).Return(stakingtypes.UnbondingDelegation{ValidatorAddress: other.GetOperator()}, nil)
			mocks.MockStakingKeeper.EXPECT().GetValidator(ctx, sdk.ValAddress(otherValAddr)).Return(other, nil)
		}, false},
		{"an operation x/staking does not know", 25, func() {
			mocks.MockStakingKeeper.EXPECT().GetUnbondingType(ctx, uint64(25)).Return(stakingtypes.UnbondingType_Undefined, stakingtypes.ErrNoUnbondingType)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.expect()
			require.NoError(t, k.Hooks().AfterUnbondingInitiated(ctx, tc.id))
			held, err := k.HeldUnbondingOps.Has(ctx, collections.Join(consAddr.Bytes(), tc.id))
			require.NoError(t, err)
			require.Equal(t, tc.held, held)
		})
	}
}

// TestPauseExtendsJailOfPendingPunishments: pausing a consumer freezes its
// pending punishments for up to MaxPauseDuration, so the pause moves the
// accused validators' jail horizon to the pause's expiration plus the margin
// in one step; the frozen sweeps then touch nothing.
func TestPauseExtendsJailOfPendingPunishments(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	const consumerID = uint64(0)
	queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, consumerID, 11)

	pauseExpiration := ctx.BlockTime().Add(k.GetMaxPauseDuration(ctx))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(ctx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(ctx, consAddr, pauseExpiration.Add(24*time.Hour))
	require.NoError(t, k.PauseConsumerChain(ctx, consumerID))

	// Frozen and matured: still nothing to do until the pause resolves.
	frozen := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Hour))
	k.SweepPendingEquivocationPunishments(frozen)
	has, err := k.PendingEquivocationPunishments.Has(frozen, collections.Join3(consumerID, consAddr.Bytes(), int64(42)))
	require.NoError(t, err)
	require.True(t, has)
}

// TestSecondQueueDoesNotShortenJail: a validator with a punishment already
// deferred far into the future is accused on a second consumer; the new
// queue-time jail must not pull the horizon back to the new entry's own,
// earlier, execution time.
func TestSecondQueueDoesNotShortenJail(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	// First entry, deferred behind a vote ending far out.
	_, entry, matured := deferEquivocationBehindVote(t, k, ctx, mocks, validator, providerAddr, 0, 23)
	farEnd := matured.BlockTime().Add(60 * 24 * time.Hour)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, _ uint64) (govv1.ProposalStatus, time.Time, bool) {
		return govv1.StatusVotingPeriod, farEnd, true
	})
	margin := k.GetParams(ctx).RemovalVoteDeferralMargin
	later := matured.WithBlockTime(entry.ExecutesAt.Add(time.Second))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(later, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(later, consAddr, farEnd.Add(margin).Add(24*time.Hour))
	k.SweepPendingEquivocationPunishments(later)

	// Second entry, on another consumer: its own horizon would be
	// EquivocationExecutionDelay + margin from now, well before the first
	// entry's; the jail keeps the later horizon.
	k.SetConsumerPhase(later, 1, types.CONSUMER_PHASE_LAUNCHED)
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(later, consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(later, consAddr).Return(false)
	mocks.MockStakingKeeper.EXPECT().Jail(later, consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(later, consAddr, farEnd.Add(margin).Add(24*time.Hour))
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(later, valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(later, valAddr).Return(nil, nil)
	_, err = k.QueuePendingEquivocationPunishment(later, 1, providerAddr, 7)
	require.NoError(t, err)
}

// TestExecutionDropsEntryOfVanishedValidator: a validator x/staking no longer
// knows cannot be punished; the entry is dropped with an event and its holds
// released instead of being retried forever.
func TestExecutionDropsEntryOfVanishedValidator(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)

	lateCtx := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(stakingtypes.Validator{}, stakingtypes.ErrNoValidatorFound)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(lateCtx, uint64(11)).Return(nil)
	k.SweepPendingEquivocationPunishments(lateCtx)

	has, err := k.PendingEquivocationPunishments.Has(lateCtx, key)
	require.NoError(t, err)
	require.False(t, has, "an entry that can never execute is dropped")
	held, err := k.HeldUnbondingOps.Has(lateCtx, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, held)
	var dropped bool
	for _, ev := range lateCtx.EventManager().Events() {
		dropped = dropped || ev.Type == types.EventTypeEquivocationPunishmentDropped
	}
	require.True(t, dropped, "the drop is announced")
}

// TestExecutionDropsSiblingEntriesOnceTombstoned: one validator accused on
// two consumers; executing the first entry tombstones it, so the second entry
// has nothing left to take and is dropped with it, releasing the holds.
func TestExecutionDropsSiblingEntriesOnceTombstoned(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	first := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)

	// The second accusation lands an hour later on another consumer.
	laterCtx := ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))
	k.SetConsumerPhase(laterCtx, 1, types.CONSUMER_PHASE_LAUNCHED)
	expectQueueJail(k, mocks, laterCtx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, laterCtx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(laterCtx, 1, providerAddr, 42)
	require.NoError(t, err)
	second := collections.Join3(uint64(1), consAddr.Bytes(), int64(42))

	// The first entry matures and executes: the validator is tombstoned.
	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	expectExecution(t, k, mocks, matured, validator, consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(matured, uint64(11)).Return(nil)
	k.SweepPendingEquivocationPunishments(matured)

	for _, key := range []collections.Triple[uint64, []byte, int64]{first, second} {
		has, err := k.PendingEquivocationPunishments.Has(matured, key)
		require.NoError(t, err)
		require.False(t, has, "both entries are gone once the validator is tombstoned")
	}
	held, err := k.HeldUnbondingOps.Has(matured, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, held)
}

// TestExecutionRetriesWhenTombstoneFails: a failure after the slash leaves
// the entry and its holds in place for the next block, where the whole
// punishment runs again.
func TestExecutionRetriesWhenTombstoneFails(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)

	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	require.NoError(t, err)
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), consAddr).Return(false).Times(2)
	mocks.MockStakingKeeper.EXPECT().GetUnbondingDelegationsFromValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().GetRedelegationsFromSrcValidator(gomock.Any(), valAddr).Return(nil, nil)
	mocks.MockStakingKeeper.EXPECT().PowerReduction(gomock.Any()).Return(math.NewInt(1))
	mocks.MockStakingKeeper.EXPECT().SlashWithInfractionReason(gomock.Any(), consAddr, int64(0), int64(500), gomock.Any(), stakingtypes.Infraction_INFRACTION_DOUBLE_SIGN).Return(math.NewInt(25), nil)
	mocks.MockStakingKeeper.EXPECT().Jail(gomock.Any(), consAddr)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(gomock.Any(), consAddr, gomock.Any())
	mocks.MockSlashingKeeper.EXPECT().Tombstone(gomock.Any(), consAddr).Return(errors.New("signing info missing"))
	k.SweepPendingEquivocationPunishments(matured)

	has, err := k.PendingEquivocationPunishments.Has(matured, key)
	require.NoError(t, err)
	require.True(t, has, "the entry waits for the next block")
	held, err := k.HeldUnbondingOps.Has(matured, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.True(t, held, "the holds stay while the punishment is pending")

	next := matured.WithBlockTime(matured.BlockTime().Add(5 * time.Second))
	expectExecution(t, k, mocks, next, validator, consAddr)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(next, uint64(11)).Return(nil)
	k.SweepPendingEquivocationPunishments(next)
	has, err = k.PendingEquivocationPunishments.Has(next, key)
	require.NoError(t, err)
	require.False(t, has)
}

// TestExecutionOfTombstonedValidatorSettles: a validator tombstoned meanwhile
// (provider-native evidence) has nothing left to take at maturity; the entry
// is settled without a second slash and its holds are released.
func TestExecutionOfTombstonedValidatorSettles(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	key := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)

	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(gomock.Any(), consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().IsTombstoned(gomock.Any(), consAddr).Return(true)
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(matured, uint64(11)).Return(nil)
	k.SweepPendingEquivocationPunishments(matured)

	has, err := k.PendingEquivocationPunishments.Has(matured, key)
	require.NoError(t, err)
	require.False(t, has, "an already tombstoned validator's entry is settled")
	held, err := k.HeldUnbondingOps.Has(matured, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.False(t, held)
}

// TestCancellingOneOfTwoEntriesKeepsHoldsAndJail: with punishments pending on
// two consumers, a governance removal of one cancels its entry only; the
// holds stay for the other and the jail shrinks to that entry's horizon.
func TestCancellingOneOfTwoEntriesKeepsHoldsAndJail(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()

	first := queueWithHeldOp(t, k, ctx, mocks, validator, providerAddr, 0, 11)
	laterCtx := ctx.WithBlockTime(ctx.BlockTime().Add(time.Hour))
	k.SetConsumerPhase(laterCtx, 1, types.CONSUMER_PHASE_LAUNCHED)
	expectQueueJail(k, mocks, laterCtx, validator, consAddr)
	expectNoExistingUnbondings(t, k, mocks, laterCtx, validator, consAddr)
	_, err := k.QueuePendingEquivocationPunishment(laterCtx, 1, providerAddr, 42)
	require.NoError(t, err)
	second, err := k.PendingEquivocationPunishments.Get(laterCtx, collections.Join3(uint64(1), consAddr.Bytes(), int64(42)))
	require.NoError(t, err)

	// Governance removes consumer 1: only its entry goes; no hold is
	// released, and the jail is set to what the first entry still requires.
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(laterCtx, consAddr).Return(validator, nil)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(laterCtx, consAddr, ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay).Add(24*time.Hour))
	k.CancelPendingEquivocationPunishmentsForConsumer(laterCtx, 1)

	has, err := k.PendingEquivocationPunishments.Has(laterCtx, first)
	require.NoError(t, err)
	require.True(t, has, "the other consumer's entry stays")
	held, err := k.HeldUnbondingOps.Has(laterCtx, collections.Join(consAddr.Bytes(), uint64(11)))
	require.NoError(t, err)
	require.True(t, held, "holds stay while any entry is pending")
	_ = second
}

// TestSweepConsultsTheVoteOncePerConsumer: a sweep over several matured
// entries of one consumer scans the active proposals a single time.
func TestSweepConsultsTheVoteOncePerConsumer(t *testing.T) {
	k, ctx, ctrl, mocks, validator, providerAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	consAddr := providerAddr.ToSdkConsAddr()
	k.SetConsumerPhase(ctx, 0, types.CONSUMER_PHASE_LAUNCHED)
	for _, height := range []int64{42, 43} {
		expectQueueJail(k, mocks, ctx, validator, consAddr)
		expectNoExistingUnbondings(t, k, mocks, ctx, validator, consAddr)
		_, err := k.QueuePendingEquivocationPunishment(ctx, 0, providerAddr, height)
		require.NoError(t, err)
	}

	matured := ctx.WithBlockTime(ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay + time.Second))
	lookups := map[uint64]int{}
	k.OverrideRemovalVoteForTest(func(_ sdk.Context, cid uint64) (uint64, time.Time, bool) {
		lookups[cid]++
		return 7, matured.BlockTime().Add(time.Hour), true
	})
	mocks.MockStakingKeeper.EXPECT().GetValidatorByConsAddr(matured, consAddr).Return(validator, nil).Times(2)
	mocks.MockSlashingKeeper.EXPECT().JailUntil(matured, consAddr, gomock.Any()).Times(2)
	k.SweepPendingEquivocationPunishments(matured)
	require.Equal(t, map[uint64]int{0: 1}, lookups, "one scan per consumer per sweep")
}

// TestSweepCancelsAndExecutesInOneBlock: two validators, two consumers, both
// deferred; in one sweep the entry whose vote passed against a stopped
// consumer is cancelled while the entry whose vote was rejected executes.
func TestSweepCancelsAndExecutesInOneBlock(t *testing.T) {
	k, ctx, ctrl, mocks, cancelledVal, cancelledAddr := setupEquivocationQueueTest(t)
	defer ctrl.Finish()
	executedVal, executedAddr := newSweepValidator(t)
	executedVal.Tokens = math.NewInt(500)
	k.SetConsumerPhase(ctx, 0, types.CONSUMER_PHASE_STOPPED)
	k.SetConsumerPhase(ctx, 1, types.CONSUMER_PHASE_LAUNCHED)

	seed := func(consumerID uint64, addr types.ProviderConsAddress, proposalID uint64, opID uint64) collections.Triple[uint64, []byte, int64] {
		key := collections.Join3(consumerID, addr.ToSdkConsAddr().Bytes(), int64(42))
		require.NoError(t, k.PendingEquivocationPunishments.Set(ctx, key, types.PendingEquivocationPunishment{
			ConsumerId:           consumerID,
			ProviderConsAddr:     addr.ToSdkConsAddr().Bytes(),
			InfractionHeight:     42,
			ExecutesAt:           ctx.BlockTime().Add(-time.Minute),
			ExecutesAtExtended:   true,
			DeferredByProposalId: proposalID,
		}))
		mocks.MockStakingKeeper.EXPECT().PutUnbondingOnHold(ctx, opID).Return(nil)
		require.NoError(t, k.HoldUnbondingOpForTest(ctx, addr.ToSdkConsAddr(), opID))
		return key
	}
	cancelledKey := seed(0, cancelledAddr, 5, 51)
	executedKey := seed(1, executedAddr, 6, 61)
	k.OverrideRemovalProposalStatusForTest(func(_ sdk.Context, proposalId uint64) (govv1.ProposalStatus, time.Time, bool) {
		switch proposalId {
		case 5:
			return govv1.StatusFailed, time.Time{}, true
		case 6:
			return govv1.StatusRejected, time.Time{}, true
		}
		t.Fatalf("unexpected proposal %d", proposalId)
		return govv1.StatusNil, time.Time{}, false
	})

	expectCancellation(mocks, ctx, cancelledVal, cancelledAddr.ToSdkConsAddr(), 51)
	expectExecution(t, k, mocks, ctx, executedVal, executedAddr.ToSdkConsAddr())
	mocks.MockStakingKeeper.EXPECT().UnbondingCanComplete(ctx, uint64(61)).Return(nil)
	k.SweepPendingEquivocationPunishments(ctx)

	for _, key := range []collections.Triple[uint64, []byte, int64]{cancelledKey, executedKey} {
		has, err := k.PendingEquivocationPunishments.Has(ctx, key)
		require.NoError(t, err)
		require.False(t, has)
	}
}
