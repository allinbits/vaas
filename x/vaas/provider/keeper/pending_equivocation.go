package keeper

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	slashingtypes "github.com/cosmos/cosmos-sdk/x/slashing/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

// pendingEquivocationJailMargin pads the jail applied at queue time past the
// pending punishment's execution (or extended execution) moment, so the
// validator cannot unjail in the gap between maturity and the sweep that
// executes it. The sweep runs every block, so the gap is normally one block;
// a day covers a chain halted around the maturity (a coordinated upgrade) and
// costs an accused validator nothing it would not lose to the tombstone.
const pendingEquivocationJailMargin = 24 * time.Hour

// QueuePendingEquivocationPunishment records a verified consumer equivocation
// for deferred execution: the validator is jailed immediately (reversible,
// see jailValidatorUntil) and its unbonding operations are put on hold, while
// the irreversible slash and tombstone wait out EquivocationExecutionDelay so
// a removal proposal against the consumer can reach its voting period first
// (see SweepPendingEquivocationPunishments for the resolution paths).
//
// The entry and its holds are keyed by the consensus address the validator
// runs now, which is not providerAddr when the evidence names a key the
// validator has since rotated away from: the unbonding hook resolves
// operations to the live address, and a rotation after the queue moves the
// state along (see MigrateStateOnConsPubKeyRotation), so the two always meet.
//
// Returns alreadyTombstoned=true when the validator is already tombstoned,
// which keeps re-submitted evidence for an already-punished validator
// idempotent. A re-submission of evidence already queued (same consumer,
// validator, and infraction height) is a no-op.
func (k Keeper) QueuePendingEquivocationPunishment(
	ctx sdk.Context,
	consumerId uint64,
	providerAddr types.ProviderConsAddress,
	infractionHeight int64,
) (alreadyTombstoned bool, err error) {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil && errors.Is(err, stakingtypes.ErrNoValidatorFound) {
		return false, errorsmod.Wrapf(slashingtypes.ErrNoValidatorForAddress, "provider consensus address: %s", providerAddr.String())
	} else if err != nil {
		return false, errorsmod.Wrapf(slashingtypes.ErrBadValidatorAddr, "unknown error looking for provider consensus address: %s", providerAddr.String())
	}
	liveAddr := types.NewProviderConsAddress(liveConsAddrOf(validator, providerAddr))
	consAddr := liveAddr.ToSdkConsAddr()

	key := collections.Join3(consumerId, consAddr.Bytes(), infractionHeight)
	if has, err := k.PendingEquivocationPunishments.Has(ctx, key); err != nil {
		return false, fmt.Errorf("checking pending equivocation punishment: %w", err)
	} else if has {
		// Already queued; still report a tombstone that landed meanwhile.
		return k.slashingKeeper.IsTombstoned(ctx, consAddr), nil
	}

	if validator.IsUnbonded() {
		return false, errorsmod.Wrapf(stakingtypes.ErrNoUnbondingDelegation, "validator is unbonded. provider consensus address: %s", providerAddr.String())
	}
	if k.slashingKeeper.IsTombstoned(ctx, consAddr) {
		return true, nil
	}

	executesAt := ctx.BlockTime().Add(k.GetParams(ctx).EquivocationExecutionDelay)
	entry := types.PendingEquivocationPunishment{
		ConsumerId:       consumerId,
		ProviderConsAddr: consAddr.Bytes(),
		InfractionHeight: infractionHeight,
		ExecutesAt:       executesAt,
	}
	if err := k.PendingEquivocationPunishments.Set(ctx, key, entry); err != nil {
		return false, fmt.Errorf("queueing equivocation punishment: %w", err)
	}

	// Jail now, tombstone later: jailing is reversible, so it can neutralize
	// the validator during the window without prejudging the evidence. The
	// tombstone decision of the double-sign parameters is deliberately not
	// consulted here; it applies at execution. The entry is stored first so
	// the jail update finds it and keeps its record.
	if !validator.IsJailed() {
		if err := k.stakingKeeper.Jail(ctx, consAddr); err != nil {
			return false, err
		}
	}
	if err := k.jailValidatorUntil(ctx, consAddr, executesAt.Add(pendingEquivocationJailMargin), false); err != nil {
		return false, fmt.Errorf("jailing for the pending equivocation punishment: %w", err)
	}

	if err := k.holdValidatorUnbondings(ctx, validator, consAddr); err != nil {
		return false, err
	}

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEquivocationPunishmentQueued,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
		sdk.NewAttribute(types.AttributeProviderValidatorAddress, liveAddr.String()),
		sdk.NewAttribute(types.AttributeExecutesAt, executesAt.String()),
	))
	return false, nil
}

// holdValidatorUnbondings puts every unbonding operation of the validator on
// hold (undelegations, redelegations out, and the validator's own unbonding),
// so the stake a pending equivocation punishment would slash cannot finish
// unbonding before the punishment resolves. Held ids are recorded under
// consAddr, the validator's live consensus address, and released by
// releaseValidatorHolds. Operations started while the punishment is pending
// are held by Hooks.AfterUnbondingInitiated.
func (k Keeper) holdValidatorUnbondings(ctx sdk.Context, validator stakingtypes.Validator, consAddr sdk.ConsAddress) error {
	valAddr, err := k.ValidatorAddressCodec().StringToBytes(validator.GetOperator())
	if err != nil {
		return fmt.Errorf("holding unbondings: %w", err)
	}

	ids := append([]uint64(nil), validator.UnbondingIds...)

	ubds, err := k.stakingKeeper.GetUnbondingDelegationsFromValidator(ctx, valAddr)
	if err != nil {
		return fmt.Errorf("holding unbondings: %w", err)
	}
	for _, ubd := range ubds {
		for _, entry := range ubd.Entries {
			ids = append(ids, entry.UnbondingId)
		}
	}
	reds, err := k.stakingKeeper.GetRedelegationsFromSrcValidator(ctx, valAddr)
	if err != nil {
		return fmt.Errorf("holding unbondings: %w", err)
	}
	for _, red := range reds {
		for _, entry := range red.Entries {
			ids = append(ids, entry.UnbondingId)
		}
	}

	for _, id := range ids {
		if err := k.holdUnbondingOp(ctx, consAddr, id); err != nil {
			return err
		}
	}
	return nil
}

// holdUnbondingOp places one hold, at most once per (validator, op). An id
// x/staking no longer indexes names an operation that already completed, so
// there is nothing left to hold: a validator's own UnbondingIds outlive the
// completion that deleted their index, and stay on the validator forever.
func (k Keeper) holdUnbondingOp(ctx sdk.Context, consAddr sdk.ConsAddress, id uint64) error {
	key := collections.Join(consAddr.Bytes(), id)
	if has, err := k.HeldUnbondingOps.Has(ctx, key); err != nil {
		return fmt.Errorf("checking unbonding hold %d: %w", id, err)
	} else if has {
		return nil
	}
	if err := k.stakingKeeper.PutUnbondingOnHold(ctx, id); err != nil {
		if unbondingOpCompleted(err) {
			return nil
		}
		return fmt.Errorf("holding unbonding op %d: %w", id, err)
	}
	if err := k.HeldUnbondingOps.Set(ctx, key); err != nil {
		return fmt.Errorf("recording unbonding hold %d: %w", id, err)
	}
	return nil
}

// unbondingOpCompleted reports whether err from PutUnbondingOnHold says the
// operation is no longer indexed by x/staking, i.e. it completed already.
func unbondingOpCompleted(err error) bool {
	return errors.Is(err, stakingtypes.ErrNoUnbondingType) ||
		errors.Is(err, stakingtypes.ErrNoValidatorFound) ||
		errors.Is(err, stakingtypes.ErrNoUnbondingDelegation) ||
		errors.Is(err, stakingtypes.ErrNoRedelegation)
}

// releaseValidatorHolds releases every unbonding hold recorded for the
// validator; called once its last pending equivocation punishment resolves. A
// release past the operation's maturity completes it immediately.
func (k Keeper) releaseValidatorHolds(ctx sdk.Context, consAddr sdk.ConsAddress) {
	rng := collections.NewPrefixedPairRange[[]byte, uint64](consAddr.Bytes())
	var ids []uint64
	if err := k.HeldUnbondingOps.Walk(ctx, rng, func(key collections.Pair[[]byte, uint64]) (bool, error) {
		ids = append(ids, key.K2())
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to iterate unbonding holds", "error", err)
		return
	}
	for _, id := range ids {
		if err := k.stakingKeeper.UnbondingCanComplete(ctx, id); err != nil {
			k.Logger(ctx).Error("failed to release unbonding hold", "id", id, "error", err)
		}
		if err := k.HeldUnbondingOps.Remove(ctx, collections.Join(consAddr.Bytes(), id)); err != nil {
			k.Logger(ctx).Error("failed to delete unbonding hold record", "id", id, "error", err)
		}
	}
}

// hasPendingEquivocationFor reports whether any pending punishment, for any
// consumer, names the validator. It walks the whole table, which the unbonding
// hook does once per new unbonding operation: pending punishments are rare and
// few, so the walk is a handful of reads, and an index by validator would only
// add state to keep consistent.
func (k Keeper) hasPendingEquivocationFor(ctx sdk.Context, consAddr sdk.ConsAddress) bool {
	found := false
	if err := k.PendingEquivocationPunishments.Walk(ctx, nil, func(key collections.Triple[uint64, []byte, int64], _ types.PendingEquivocationPunishment) (bool, error) {
		if sdk.ConsAddress(key.K2()).Equals(consAddr) {
			found = true
			return true, nil
		}
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to scan pending equivocation punishments", "error", err)
	}
	return found
}

// SweepPendingEquivocationPunishments resolves matured pending equivocation
// punishments each BeginBlock:
//
//   - a PAUSED consumer freezes its entries in place: the pause is a
//     governance deliberation window (resume or removal), and executing an
//     irreversible punishment inside it would pre-empt the verdict;
//   - a matured entry whose consumer has a removal vote in progress is
//     deferred past the voting end, recording the proposal it deferred
//     behind, mirroring the downtime deferral;
//   - a deferred entry is decided by that proposal's tally: a passed vote
//     cancels it (holds released, jail opened), whether its MsgRemoveConsumer
//     executed or found the consumer already stopped; a rejected vote lets it
//     execute; a vote still open at the extended maturity keeps deferring it,
//     and no other proposal ever does;
//   - any other matured entry executes: the validator is slashed and
//     tombstoned at the double-sign parameters, and the holds are released so
//     the (now slashed) unbonding operations complete.
//
// Only a governance removal cancels a pending punishment: the removal
// executing (see the RemoveConsumer handler) or the removal vote an entry
// deferred behind passing. A consumer stopped for liveness or for a lapsed
// pause is not a verdict on its evidence, and letting such stops cancel
// would hand a coalition able to halt the chain a way to void punishments;
// entries of a STOPPED or DELETED consumer therefore keep running on their
// own clock. This is deliberately stricter than downtime, whose pending
// slashes any stop does cancel: a downtime accusation can only be disproved
// by a challenge that needs the accused chain alive to fetch headers from,
// while equivocation evidence is self-contained signatures that need nothing
// from the chain to stand.
func (k Keeper) SweepPendingEquivocationPunishments(ctx sdk.Context) {
	type item struct {
		key    collections.Triple[uint64, []byte, int64]
		entry  types.PendingEquivocationPunishment
		reason string
	}
	var toCancel, toExtend, toExecute []item
	votes := k.newRemovalVoteMemo()
	margin := k.GetParams(ctx).RemovalVoteDeferralMargin

	if err := k.PendingEquivocationPunishments.Walk(ctx, nil, func(key collections.Triple[uint64, []byte, int64], entry types.PendingEquivocationPunishment) (bool, error) {
		consumerId := key.K1()
		if k.GetConsumerPhase(ctx, consumerId) == types.CONSUMER_PHASE_PAUSED {
			// Frozen. The jail horizon was moved to the pause's expiration
			// when the consumer was paused (see PauseConsumerChain), so the
			// accused validator cannot unjail into the active set mid-pause,
			// and the resume moves the execution time past the pause
			// (thawConsumerAccusations).
			return false, nil
		}
		if entry.ExecutesAt.After(ctx.BlockTime()) {
			return false, nil
		}
		if entry.ExecutesAtExtended {
			status, votingEnd, found := k.removalProposalStatus(ctx, entry.DeferredByProposalId)
			switch {
			case found && status == govv1.StatusVotingPeriod:
				// The vote is still open at the extended maturity: gov moved
				// its end after a late quorum, or no block landed inside the
				// margin. The same proposal keeps shielding the entry until
				// its tally is in.
				toExtend = append(toExtend, item{key: key, entry: deferredBehind(entry, entry.DeferredByProposalId, votingEnd, margin)})
			case found && status == govv1.StatusPassed:
				toCancel = append(toCancel, item{key: key, entry: entry, reason: "removal vote passed"})
			case found && status == govv1.StatusFailed && k.consumerStopped(ctx, consumerId):
				// A passed vote whose MsgRemoveConsumer found nothing left to
				// remove: the consumer stopped for another reason during the
				// vote. The tally is the verdict.
				toCancel = append(toCancel, item{key: key, entry: entry, reason: "removal vote passed after the consumer had stopped"})
			default:
				toExecute = append(toExecute, item{key: key, entry: entry})
			}
			return false, nil
		}
		if v := votes.get(ctx, consumerId); v.active {
			toExtend = append(toExtend, item{key: key, entry: deferredBehind(entry, v.proposalId, v.end, margin)})
			return false, nil
		}
		toExecute = append(toExecute, item{key: key, entry: entry})
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to iterate pending equivocation punishments", "error", err)
		return
	}

	for _, it := range toExtend {
		if err := k.PendingEquivocationPunishments.Set(ctx, it.key, it.entry); err != nil {
			k.Logger(ctx).Error("failed to defer pending equivocation punishment", "error", err)
			continue
		}
		// Keep the queue-time jail closed until the extended execution.
		k.refreshPendingEquivocationJail(ctx, types.NewProviderConsAddress(sdk.ConsAddress(it.key.K2())))
		k.Logger(ctx).Info("pending equivocation punishment deferred behind a removal vote",
			"consumerId", it.key.K1(),
			"proposalId", it.entry.DeferredByProposalId,
			"executesAt", it.entry.ExecutesAt,
		)
	}

	for _, it := range toExecute {
		k.executePendingEquivocation(ctx, it.key)
	}
	for _, it := range toCancel {
		k.cancelPendingEquivocation(ctx, it.key, it.reason)
	}
}

// deferredBehind returns entry pushed past the end of proposalId's removal
// vote: its maturity becomes the later of its current value and the voting
// end plus margin, and the proposal is recorded as the one it waits for.
func deferredBehind(entry types.PendingEquivocationPunishment, proposalId uint64, votingEnd time.Time, margin time.Duration) types.PendingEquivocationPunishment {
	if newMaturity := votingEnd.Add(margin); newMaturity.After(entry.ExecutesAt) {
		entry.ExecutesAt = newMaturity
	}
	entry.ExecutesAtExtended = true
	entry.DeferredByProposalId = proposalId
	return entry
}

// consumerStopped reports whether the consumer has left the phases a
// governance removal can act on: STOPPED or DELETED.
func (k Keeper) consumerStopped(ctx sdk.Context, consumerId uint64) bool {
	switch k.GetConsumerPhase(ctx, consumerId) {
	case types.CONSUMER_PHASE_STOPPED, types.CONSUMER_PHASE_DELETED:
		return true
	default:
		return false
	}
}

// executePendingEquivocation applies the deferred slash and tombstone as one
// unit, then removes the entry and releases the validator's holds. A
// validator already tombstoned (by another entry, or by provider-native
// evidence) has nothing left to take; one that x/staking no longer knows, or
// that fully unbonded, cannot be punished at all, so its entry is dropped
// with an event rather than retried forever with its holds pinned. Any other
// failure leaves the entry for the next block: the jail and holds keep the
// state safe meanwhile. Once the validator is tombstoned its other pending
// entries are moot and are dropped too, so their holds do not outlive the
// punishment.
// pendingEquivocationExists reports whether the entry is still pending. The
// sweep collects its keys up front and one resolution can remove others of
// the same validator (a tombstone drops its siblings), so every resolution
// path checks before acting: a key already gone is settled, not re-executed,
// re-cancelled or re-dropped.
func (k Keeper) pendingEquivocationExists(ctx sdk.Context, key collections.Triple[uint64, []byte, int64]) bool {
	has, err := k.PendingEquivocationPunishments.Has(ctx, key)
	if err != nil {
		k.Logger(ctx).Error("failed to read pending equivocation punishment", "error", err)
		return false
	}
	return has
}

func (k Keeper) executePendingEquivocation(ctx sdk.Context, key collections.Triple[uint64, []byte, int64]) {
	entry, err := k.PendingEquivocationPunishments.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			k.Logger(ctx).Error("failed to read pending equivocation punishment", "error", err)
		}
		return
	}
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(key.K2()))
	infractionParams := k.GetInfractionParams(ctx)

	cacheCtx, write := ctx.CacheContext()
	alreadyTombstoned, err := k.punishEquivocation(cacheCtx, providerAddr, infractionParams.DoubleSign)
	switch {
	case err == nil && alreadyTombstoned:
		// Already punished: the entry is settled without a second slash.
	case err == nil:
		write()
	case errors.Is(err, slashingtypes.ErrNoValidatorForAddress), errors.Is(err, stakingtypes.ErrNoUnbondingDelegation):
		k.dropPendingEquivocation(ctx, key, err.Error())
		return
	case ctx.BlockTime().After(entry.ExecutesAt.Add(pendingEquivocationJailMargin)):
		// Retried every block since maturity and still failing: a fault this
		// persistent is not going away by itself, and the holds behind the
		// entry freeze every delegator's unbondings for as long as it stays.
		// The entry is dropped, its holds released, the jail left as it is.
		k.dropPendingEquivocation(ctx, key, "execution kept failing past the jail margin: "+err.Error())
		return
	default:
		k.Logger(ctx).Error("failed to execute pending equivocation punishment; retrying next block",
			"consumerId", key.K1(), "providerAddr", providerAddr.String(), "error", err)
		return
	}

	if err := k.PendingEquivocationPunishments.Remove(ctx, key); err != nil {
		k.Logger(ctx).Error("failed to delete executed equivocation punishment", "error", err)
	}
	consAddr := providerAddr.ToSdkConsAddr()
	// A tombstone ends the validator's punishability, so its other entries go
	// with this one; without it each remaining entry is a further infraction
	// that executes on its own, and the holds stay until the last one has.
	if alreadyTombstoned || infractionParams.DoubleSign.Tombstone {
		k.dropEntriesOfTombstonedValidator(ctx, consAddr)
	}
	k.settleValidatorWithNothingPending(ctx, consAddr)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEquivocationPunishmentExecuted,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", key.K1())),
		sdk.NewAttribute(types.AttributeProviderValidatorAddress, providerAddr.String()),
	))
	k.Logger(ctx).Info("pending equivocation punishment executed",
		"consumerId", key.K1(),
		"providerAddr", providerAddr.String(),
	)
}

// dropEntriesOfTombstonedValidator removes the validator's remaining pending
// punishments once it is tombstoned: nothing more can be taken from it, and
// keeping them would only keep its delegators' unbonding operations on hold.
func (k Keeper) dropEntriesOfTombstonedValidator(ctx sdk.Context, consAddr sdk.ConsAddress) {
	var keys []collections.Triple[uint64, []byte, int64]
	if err := k.PendingEquivocationPunishments.Walk(ctx, nil, func(key collections.Triple[uint64, []byte, int64], _ types.PendingEquivocationPunishment) (bool, error) {
		if sdk.ConsAddress(key.K2()).Equals(consAddr) {
			keys = append(keys, key)
		}
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to scan pending equivocation punishments", "error", err)
		return
	}
	for _, key := range keys {
		k.dropPendingEquivocation(ctx, key, "validator tombstoned")
	}
}

// dropPendingEquivocation removes one pending punishment that can no longer
// be executed, releasing the validator's holds when it was the last one, and
// says why in an event. Unlike a cancellation it is not a verdict on the
// evidence, so the jail is left as it stands.
func (k Keeper) dropPendingEquivocation(ctx sdk.Context, key collections.Triple[uint64, []byte, int64], reason string) {
	if !k.pendingEquivocationExists(ctx, key) {
		return
	}
	if err := k.PendingEquivocationPunishments.Remove(ctx, key); err != nil {
		k.Logger(ctx).Error("failed to delete dropped equivocation punishment", "error", err)
		return
	}
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(key.K2()))
	consAddr := providerAddr.ToSdkConsAddr()
	k.settleValidatorWithNothingPending(ctx, consAddr)
	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEquivocationPunishmentDropped,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", key.K1())),
		sdk.NewAttribute(types.AttributeProviderValidatorAddress, providerAddr.String()),
		sdk.NewAttribute(vaastypes.AttributeDropReason, reason),
	))
	k.Logger(ctx).Info("pending equivocation punishment dropped",
		"consumerId", key.K1(),
		"providerAddr", providerAddr.String(),
		"reason", reason,
	)
}

// cancelPendingEquivocation removes one pending punishment on a governance
// verdict, shrinks the jail to what the validator's remaining entries require
// (down to what it owes for other reasons when none remain, so it can unjail
// at once if it owes nothing), and releases its holds when it was its last
// one. The jail is refreshed before the holds are settled: the refresh tells
// the jail this module set from everyone else's by the record the settlement
// drops.
func (k Keeper) cancelPendingEquivocation(ctx sdk.Context, key collections.Triple[uint64, []byte, int64], reason string) {
	if !k.pendingEquivocationExists(ctx, key) {
		return
	}
	if err := k.PendingEquivocationPunishments.Remove(ctx, key); err != nil {
		k.Logger(ctx).Error("failed to delete cancelled equivocation punishment", "error", err)
		return
	}
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(key.K2()))
	consAddr := providerAddr.ToSdkConsAddr()
	k.refreshPendingEquivocationJail(ctx, providerAddr)
	k.settleValidatorWithNothingPending(ctx, consAddr)

	ctx.EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeEquivocationPunishmentCancelled,
		sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
		sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", key.K1())),
		sdk.NewAttribute(types.AttributeProviderValidatorAddress, providerAddr.String()),
		sdk.NewAttribute(types.AttributeCancelReason, reason),
	))
	k.Logger(ctx).Info("pending equivocation punishment cancelled",
		"consumerId", key.K1(),
		"providerAddr", providerAddr.String(),
		"reason", reason,
	)
}

// CancelPendingEquivocationPunishmentsForConsumer cancels every pending
// punishment sourced from the consumer; called when governance removes the
// consumer (the RemoveConsumer handler), which is the one verdict that voids
// its evidence. Stops for any other reason must not call this; see
// SweepPendingEquivocationPunishments.
func (k Keeper) CancelPendingEquivocationPunishmentsForConsumer(ctx sdk.Context, consumerId uint64) {
	var keys []collections.Triple[uint64, []byte, int64]
	rng := collections.NewPrefixedTripleRange[uint64, []byte, int64](consumerId)
	if err := k.PendingEquivocationPunishments.Walk(ctx, rng, func(key collections.Triple[uint64, []byte, int64], _ types.PendingEquivocationPunishment) (bool, error) {
		keys = append(keys, key)
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to iterate pending equivocation punishments for cancel", "error", err)
		return
	}
	for _, key := range keys {
		k.cancelPendingEquivocation(ctx, key, "consumer removed by governance")
	}
}

// pendingEquivocationJailHorizon reports how long the validator's jail must
// stay closed on account of its pending punishments: the latest execution
// time among its entries, where an entry frozen by its consumer's pause counts
// as the later of that and the pause's expiration, plus the standard margin.
// A maturity already in the past counts as now, the entry being decided by
// the next sweep. ok is false when the validator has nothing pending.
func (k Keeper) pendingEquivocationJailHorizon(ctx sdk.Context, consAddr sdk.ConsAddress) (horizon time.Time, ok bool) {
	if err := k.PendingEquivocationPunishments.Walk(ctx, nil, func(key collections.Triple[uint64, []byte, int64], entry types.PendingEquivocationPunishment) (bool, error) {
		if !sdk.ConsAddress(key.K2()).Equals(consAddr) {
			return false, nil
		}
		until := entry.ExecutesAt
		if k.GetConsumerPhase(ctx, key.K1()) == types.CONSUMER_PHASE_PAUSED {
			if expiration, err := k.GetConsumerPauseExpirationTime(ctx, key.K1()); err == nil && expiration.After(until) {
				until = expiration
			}
		}
		if until.After(horizon) {
			horizon = until
		}
		ok = true
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to scan pending equivocation punishments", "error", err)
	}
	if !ok {
		return time.Time{}, false
	}
	if horizon.Before(ctx.BlockTime()) {
		horizon = ctx.BlockTime()
	}
	return horizon.Add(pendingEquivocationJailMargin), true
}

// refreshPendingEquivocationJail sets the validator's jail to what its
// pending punishments require (see pendingEquivocationJailHorizon), down to
// what it owes for other reasons when none remain. The address is resolved
// the way JailAndTombstoneValidator resolves it.
func (k Keeper) refreshPendingEquivocationJail(ctx sdk.Context, providerAddr types.ProviderConsAddress) {
	validator, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, providerAddr.ToSdkConsAddr())
	if err != nil {
		k.Logger(ctx).Error("failed to resolve validator for jail update", "error", err)
		return
	}
	liveAddr := liveConsAddrOf(validator, providerAddr)
	if err := k.jailValidatorUntil(ctx, liveAddr, time.Time{}, false); err != nil {
		k.Logger(ctx).Error("failed to update jail for pending equivocation", "error", err)
	}
}

// jailValidatorUntil moves the validator's jail end to the latest of until,
// the horizon its pending punishments require and the jail it owes for other
// reasons, and records what it set (PendingEquivocationJails). x/slashing
// holds a single jail end per validator, shared with every other reason it
// may be jailed for, and the record is what lets the next update tell the
// jail this module closed on account of the pending punishments, which a
// cancellation may open again, from a jail it must never shorten: a current
// end later than the one last recorded was set by someone else (provider
// downtime, say) and becomes owed, as does the end found in place at the
// first update. owed marks until itself as owed regardless of the pending
// punishments, which is what a punishment executing passes; the queue-time
// jail and the refresh pass false. The record lives exactly as long as the
// validator has pending punishments.
func (k Keeper) jailValidatorUntil(ctx sdk.Context, consAddr sdk.ConsAddress, until time.Time, owed bool) error {
	record, err := k.PendingEquivocationJails.Get(ctx, consAddr.Bytes())
	recorded := err == nil
	if err != nil && !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("reading the pending equivocation jail record: %w", err)
	}
	current, err := k.currentJailedUntil(ctx, consAddr)
	if err != nil {
		return err
	}
	if !recorded || current.After(record.JailedUntil) {
		record.OwedUntil = laterOf(record.OwedUntil, current)
	}
	if owed {
		record.OwedUntil = laterOf(record.OwedUntil, until)
	}
	target := laterOf(laterOf(ctx.BlockTime(), record.OwedUntil), until)
	if horizon, ok := k.pendingEquivocationJailHorizon(ctx, consAddr); ok {
		target = laterOf(target, horizon)
	}
	if err := k.slashingKeeper.JailUntil(ctx, consAddr, target); err != nil {
		return err
	}
	if !k.hasPendingEquivocationFor(ctx, consAddr) {
		return k.PendingEquivocationJails.Remove(ctx, consAddr.Bytes())
	}
	record.ProviderConsAddr = consAddr.Bytes()
	record.JailedUntil = target
	return k.PendingEquivocationJails.Set(ctx, consAddr.Bytes(), record)
}

// currentJailedUntil reads the jail end x/slashing holds for the validator:
// zero when it has no signing info, and an error for any other failure, which
// the caller must not read as "no jail".
func (k Keeper) currentJailedUntil(ctx sdk.Context, consAddr sdk.ConsAddress) (time.Time, error) {
	info, err := k.slashingKeeper.GetValidatorSigningInfo(ctx, consAddr)
	switch {
	case errors.Is(err, slashingtypes.ErrNoSigningInfoFound), errors.Is(err, collections.ErrNotFound):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, fmt.Errorf("reading the validator's signing info: %w", err)
	}
	return info.JailedUntil, nil
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// settleValidatorWithNothingPending releases the validator's unbonding holds
// and drops its jail record once its last pending punishment is gone; a no-op
// while any remains.
func (k Keeper) settleValidatorWithNothingPending(ctx sdk.Context, consAddr sdk.ConsAddress) {
	if k.hasPendingEquivocationFor(ctx, consAddr) {
		return
	}
	k.releaseValidatorHolds(ctx, consAddr)
	if err := k.PendingEquivocationJails.Remove(ctx, consAddr.Bytes()); err != nil {
		k.Logger(ctx).Error("failed to delete the pending equivocation jail record", "error", err)
	}
}

// refreshPendingEquivocationJailsForConsumer refreshes the jail of every
// validator with a pending punishment sourced from the consumer. Called when
// the consumer is paused, since the pause freezes those entries for up to
// MaxPauseDuration and the queue-time jail, sized for ExecutesAt, would
// otherwise open mid-pause with the punishment still pending; and when it is
// resumed, so the jail follows the execution time the thaw moved instead of
// outlasting it until the pause's former expiration.
func (k Keeper) refreshPendingEquivocationJailsForConsumer(ctx sdk.Context, consumerId uint64) {
	seen := map[string]bool{}
	var addrs []sdk.ConsAddress
	rng := collections.NewPrefixedTripleRange[uint64, []byte, int64](consumerId)
	if err := k.PendingEquivocationPunishments.Walk(ctx, rng, func(key collections.Triple[uint64, []byte, int64], _ types.PendingEquivocationPunishment) (bool, error) {
		if !seen[string(key.K2())] {
			seen[string(key.K2())] = true
			addrs = append(addrs, sdk.ConsAddress(key.K2()))
		}
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("failed to iterate pending equivocation punishments", "error", err)
		return
	}
	for _, addr := range addrs {
		k.refreshPendingEquivocationJail(ctx, types.NewProviderConsAddress(addr))
	}
}

// unbondingOpValidator resolves an unbonding-operation id to the consensus
// address of the validator whose stake it moves. found=false when the op (or
// its validator) cannot be resolved, which the caller treats as "nothing to
// hold" rather than an error: the id is guaranteed fresh by the hook, so an
// unresolvable op belongs to no accused validator.
func (k Keeper) unbondingOpValidator(ctx sdk.Context, id uint64) (sdk.ConsAddress, bool, error) {
	unbondingType, err := k.stakingKeeper.GetUnbondingType(ctx, id)
	if err != nil {
		return nil, false, nil
	}

	var operator string
	switch unbondingType {
	case stakingtypes.UnbondingType_UnbondingDelegation:
		ubd, err := k.stakingKeeper.GetUnbondingDelegationByUnbondingID(ctx, id)
		if err != nil {
			return nil, false, nil
		}
		operator = ubd.ValidatorAddress
	case stakingtypes.UnbondingType_Redelegation:
		red, err := k.stakingKeeper.GetRedelegationByUnbondingID(ctx, id)
		if err != nil {
			return nil, false, nil
		}
		operator = red.ValidatorSrcAddress
	case stakingtypes.UnbondingType_ValidatorUnbonding:
		validator, err := k.stakingKeeper.GetValidatorByUnbondingID(ctx, id)
		if err != nil {
			return nil, false, nil
		}
		consAddr, err := validator.GetConsAddr()
		if err != nil {
			return nil, false, fmt.Errorf("resolving unbonding validator consensus address: %w", err)
		}
		return consAddr, true, nil
	default:
		return nil, false, nil
	}

	valAddr, err := k.ValidatorAddressCodec().StringToBytes(operator)
	if err != nil {
		return nil, false, fmt.Errorf("resolving unbonding operator %q: %w", operator, err)
	}
	validator, err := k.stakingKeeper.GetValidator(ctx, valAddr)
	if err != nil {
		return nil, false, nil
	}
	consAddr, err := validator.GetConsAddr()
	if err != nil {
		return nil, false, fmt.Errorf("resolving validator consensus address: %w", err)
	}
	return consAddr, true, nil
}

// migratePendingEquivocations moves a rotating validator's pending
// equivocation punishments and unbonding holds from its old provider
// consensus address to its new one, for every consumer including those
// already deleted (entries outlive a deletion). Both collections are keyed by
// the validator's live address, which the rotation just changed; leaving them
// behind would hide the entries from the unbonding hook and from the
// release-when-last accounting (see QueuePendingEquivocationPunishment).
// Failures are logged: the caller runs in EndBlock, where an error would halt
// the chain.
func (k Keeper) migratePendingEquivocations(ctx sdk.Context, oldProviderAddr, newProviderAddr types.ProviderConsAddress) {
	oldAddrBz := oldProviderAddr.ToSdkConsAddr().Bytes()
	newAddrBz := newProviderAddr.ToSdkConsAddr().Bytes()

	var keys []collections.Triple[uint64, []byte, int64]
	var entries []types.PendingEquivocationPunishment
	if err := k.PendingEquivocationPunishments.Walk(ctx, nil, func(key collections.Triple[uint64, []byte, int64], entry types.PendingEquivocationPunishment) (bool, error) {
		if bytes.Equal(key.K2(), oldAddrBz) {
			keys = append(keys, key)
			entries = append(entries, entry)
		}
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's pending equivocation punishments",
			"providerConsAddr", oldProviderAddr.String(), "error", err)
	}
	for i, key := range keys {
		entry := entries[i]
		entry.ProviderConsAddr = newAddrBz
		if err := k.PendingEquivocationPunishments.Set(ctx, collections.Join3(key.K1(), newAddrBz, key.K3()), entry); err != nil {
			k.Logger(ctx).Error("cannot move pending equivocation punishment to the rotated provider consensus address",
				"consumerId", key.K1(), "providerConsAddr", newProviderAddr.String(), "error", err)
			continue
		}
		if err := k.PendingEquivocationPunishments.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("cannot delete pending equivocation punishment left at the old provider consensus address",
				"consumerId", key.K1(), "providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	}

	if record, err := k.PendingEquivocationJails.Get(ctx, oldAddrBz); err == nil {
		record.ProviderConsAddr = newAddrBz
		if err := k.PendingEquivocationJails.Set(ctx, newAddrBz, record); err != nil {
			k.Logger(ctx).Error("cannot move the pending equivocation jail record to the rotated provider consensus address",
				"providerConsAddr", newProviderAddr.String(), "error", err)
		} else if err := k.PendingEquivocationJails.Remove(ctx, oldAddrBz); err != nil {
			k.Logger(ctx).Error("cannot delete the pending equivocation jail record left at the old provider consensus address",
				"providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		k.Logger(ctx).Error("cannot read the rotating validator's pending equivocation jail record",
			"providerConsAddr", oldProviderAddr.String(), "error", err)
	}

	var ids []uint64
	if err := k.HeldUnbondingOps.Walk(ctx, collections.NewPrefixedPairRange[[]byte, uint64](oldAddrBz), func(key collections.Pair[[]byte, uint64]) (bool, error) {
		ids = append(ids, key.K2())
		return false, nil
	}); err != nil {
		k.Logger(ctx).Error("cannot read the rotating validator's unbonding holds",
			"providerConsAddr", oldProviderAddr.String(), "error", err)
	}
	for _, id := range ids {
		if err := k.HeldUnbondingOps.Set(ctx, collections.Join(newAddrBz, id)); err != nil {
			k.Logger(ctx).Error("cannot move unbonding hold to the rotated provider consensus address",
				"id", id, "providerConsAddr", newProviderAddr.String(), "error", err)
			continue
		}
		if err := k.HeldUnbondingOps.Remove(ctx, collections.Join(oldAddrBz, id)); err != nil {
			k.Logger(ctx).Error("cannot delete unbonding hold left at the old provider consensus address",
				"id", id, "providerConsAddr", oldProviderAddr.String(), "error", err)
		}
	}
}
