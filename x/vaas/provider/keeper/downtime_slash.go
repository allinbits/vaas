package keeper

import (
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// SweepPendingDowntimeSlashes executes pending downtime slashes whose
// challenge window has elapsed. It converts the receipt-time token amount
// into a stake fraction, capped by InfractionParameters.Downtime.SlashFraction,
// and never jails. Entries for validators that unbonded or were tombstoned
// in the meantime are dropped with an event.
//
// More than one entry can be pending -- and mature in the same sweep -- for
// the same (consumer, validator) pair, one per accepted disjoint window; each
// matured entry is executed independently. Once a sweep executes at least one
// slash for a pair and that pair has no pending entries left afterward, its
// WithheldFeeRecord is deleted: the executed accusations were never
// disproven, so the withheld funds simply stay with the consumer (see
// PayWithheldFees for the challenge-side counterpart, which instead pays the
// record out). A pair whose remaining entries were only dropped (never
// executed) does not trigger this; its withheld record still ages out on its
// own expiry via SweepExpiredWithheldFeeRecords (fees.go).
//
// A matured entry whose consumer has a removal vote in progress is deferred
// instead of executed: its maturity moves past the voting end by the
// RemovalVoteDeferralMargin param, recording the proposal it waits for, so
// the community's verdict on the chain decides first. A passed removal stops
// the consumer and cancels its pending slashes; a rejected one lets the
// deferred entries execute at their extended maturity. A vote still open at
// that point (gov extended it after a late quorum, or no block landed inside
// the margin) keeps deferring the entry until its tally, but only that one
// proposal ever does: serial removal proposals cannot defer a slash
// indefinitely, so a validator refusing a chain whose removal keeps failing
// is bounded by one voting period of grace per accusation. The pair's
// withheld fee record stays claimable for as long as the entry is pending,
// so a challenge won during the deferral still repays it.
func (k Keeper) SweepPendingDowntimeSlashes(ctx sdk.Context) {
	ip := k.GetInfractionParams(ctx)

	iter, err := k.PendingDowntimeSlashes.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("failed to iterate pending downtime slashes", "error", err)
		return
	}

	var maturedKeys []collections.Triple[uint64, []byte, int64]
	var maturedEntries []types.PendingDowntimeSlash
	var deferredKeys []collections.Triple[uint64, []byte, int64]
	var deferredEntries []types.PendingDowntimeSlash
	votes := k.newRemovalVoteMemo()
	margin := k.GetParams(ctx).RemovalVoteDeferralMargin
	deferBehind := func(key collections.Triple[uint64, []byte, int64], entry types.PendingDowntimeSlash, proposalId uint64, votingEnd time.Time) {
		if newMaturity := votingEnd.Add(margin); newMaturity.After(entry.MaturesAt) {
			entry.MaturesAt = newMaturity
		}
		entry.MaturesAtExtended = true
		entry.DeferredByProposalId = proposalId
		deferredKeys = append(deferredKeys, key)
		deferredEntries = append(deferredEntries, entry)
	}
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			k.Logger(ctx).Error("failed to read pending downtime slash entry", "error", err)
			continue
		}
		if kv.Value.MaturesAt.After(ctx.BlockTime()) {
			continue
		}
		consumerId := kv.Key.K1()
		if kv.Value.MaturesAtExtended {
			// Only the proposal the entry deferred behind can keep it
			// waiting, and only while its vote is still open. A passed
			// removal has already cancelled the consumer's downtime state;
			// anything else lets the entry execute.
			if status, votingEnd, found := k.removalProposalStatus(ctx, kv.Value.DeferredByProposalId); found && status == govv1.StatusVotingPeriod {
				deferBehind(kv.Key, kv.Value, kv.Value.DeferredByProposalId, votingEnd)
				continue
			}
		} else if v := votes.get(ctx, consumerId); v.active {
			deferBehind(kv.Key, kv.Value, v.proposalId, v.end)
			continue
		}
		maturedKeys = append(maturedKeys, kv.Key)
		maturedEntries = append(maturedEntries, kv.Value)
	}
	iter.Close()

	for i, key := range deferredKeys {
		if err := k.PendingDowntimeSlashes.Set(ctx, key, deferredEntries[i]); err != nil {
			k.Logger(ctx).Error("failed to defer pending downtime slash behind a removal vote", "error", err)
			continue
		}
		k.extendWithheldFeeRecord(ctx, key.K1(), key.K2(), deferredEntries[i].MaturesAt)
		k.Logger(ctx).Info("pending downtime slash deferred behind a removal vote",
			"consumerId", key.K1(),
			"proposalId", deferredEntries[i].DeferredByProposalId,
			"maturesAt", deferredEntries[i].MaturesAt,
		)
	}

	type pairKey struct {
		consumerId uint64
		addr       string
	}
	executedPairs := map[pairKey]bool{}
	seenPair := map[pairKey]bool{}
	var touchedPairs []pairKey

	for i, key := range maturedKeys {
		pk := pairKey{key.K1(), string(key.K2())}
		if !seenPair[pk] {
			seenPair[pk] = true
			touchedPairs = append(touchedPairs, pk)
		}
		if k.executeDowntimeSlash(ctx, key, maturedEntries[i], ip) {
			executedPairs[pk] = true
		}
	}

	for _, key := range maturedKeys {
		if err := k.PendingDowntimeSlashes.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete matured pending downtime slash", "error", err)
		}
	}

	// Delete-on-last-execute: check, only for pairs that saw an actual
	// execution this sweep, whether they now have zero pending entries left
	// (there may be other, still-unmatured windows for the same pair).
	for _, pk := range touchedPairs {
		if !executedPairs[pk] {
			continue
		}
		addrBytes := []byte(pk.addr)
		hasPending, err := k.hasPendingDowntimeSlash(ctx, pk.consumerId, addrBytes)
		if err != nil {
			k.Logger(ctx).Error("failed to check remaining pending downtime slashes for pair", "error", err)
			continue
		}
		if hasPending {
			continue
		}
		if err := k.WithheldFeeRecords.Remove(ctx, collections.Join(pk.consumerId, addrBytes)); err != nil && !errors.Is(err, collections.ErrNotFound) {
			k.Logger(ctx).Error("failed to delete withheld fee record after last pending downtime slash executed", "error", err)
		}
	}
}

// PruneAcceptedDowntimeWindows deletes AcceptedDowntimeWindows records whose
// acceptance is older than DowntimeChallengeWindow + DowntimeEvidenceMaxAge
// and whose pending slash, if one is still queued, has resolved, then
// advances each affected pair's DowntimeWindowFloors entry to the highest
// pruned window end, so acceptance state stays bounded while re-acceptance
// stays impossible.
//
// Pruning is sound unconditionally: any window intersecting a pruned record
// has window_start <= that record's window end <= the pair's floor, so the
// floor check rejects it outright, regardless of timestamps or parameter
// configuration. Ancient windows hit the floor, live windows hit the
// retained records; no window is ever accepted twice.
func (k Keeper) PruneAcceptedDowntimeWindows(ctx sdk.Context, ip types.InfractionParameters) {
	horizon := ip.DowntimeChallengeWindow + ip.DowntimeEvidenceMaxAge

	iter, err := k.AcceptedDowntimeWindows.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("failed to iterate accepted downtime windows", "error", err)
		return
	}

	var prunedKeys []collections.Triple[uint64, []byte, int64]
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			k.Logger(ctx).Error("failed to read accepted downtime window", "error", err)
			continue
		}
		if ctx.BlockTime().Sub(kv.Value.AcceptedAt) <= horizon {
			continue
		}
		// A pending downtime slash always has its accepted record: pruning
		// waits until the pending entry with the same key executes or is
		// cancelled, so a governance shrink of the retention horizon cannot
		// strand a still-maturing slash without the window vouching for it.
		hasPending, err := k.PendingDowntimeSlashes.Has(ctx, kv.Key)
		if err != nil {
			k.Logger(ctx).Error("failed to check pending downtime slash for accepted window", "error", err)
			continue
		}
		if hasPending {
			continue
		}
		prunedKeys = append(prunedKeys, kv.Key)
	}
	iter.Close()

	for _, key := range prunedKeys {
		pairKey := collections.Join(key.K1(), key.K2())
		floor, err := k.DowntimeWindowFloors.Get(ctx, pairKey)
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			k.Logger(ctx).Error("failed to read downtime window floor", "error", err)
			continue
		}
		// Advance the floor before deleting the record: were the record
		// deleted first and the floor write to fail, the pruned window would
		// become re-acceptable.
		if errors.Is(err, collections.ErrNotFound) || key.K3() > floor {
			if err := k.DowntimeWindowFloors.Set(ctx, pairKey, key.K3()); err != nil {
				k.Logger(ctx).Error("failed to advance downtime window floor", "error", err)
				continue
			}
		}
		if err := k.AcceptedDowntimeWindows.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete pruned accepted downtime window", "error", err)
		}
	}
}

// hasPendingDowntimeSlash reports whether (consumerId, providerConsAddr) has
// any remaining entry in PendingDowntimeSlashes, across every window-end
// height.
func (k Keeper) hasPendingDowntimeSlash(ctx sdk.Context, consumerId uint64, providerConsAddr []byte) (bool, error) {
	iter, err := k.PendingDowntimeSlashes.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerConsAddr),
	)
	if err != nil {
		return false, err
	}
	defer iter.Close()
	return iter.Valid(), nil
}

// executeDowntimeSlash executes a single matured downtime slash entry,
// reporting whether it actually executed a slash (as opposed to dropping the
// entry). It converts entry.SlashTokens into a stake fraction against the
// validator's current slashable stake, capped by ip.Downtime.SlashFraction,
// and calls SlashWithInfractionReason -- never Jail. Entries whose validator
// has since unbonded, been tombstoned, or vanished are dropped with an event
// instead of slashed; so is an entry with a zero slash amount or a validator
// whose slashable stake computes to zero tokens. A staking error from
// SlashWithInfractionReason itself is also reported via the dropped event.
func (k Keeper) executeDowntimeSlash(ctx sdk.Context, key collections.Triple[uint64, []byte, int64], entry types.PendingDowntimeSlash, ip types.InfractionParameters) bool {
	consumerId := key.K1()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(key.K2()))

	if entry.SlashTokens.IsZero() {
		k.Logger(ctx).Info(
			"dropping matured downtime slash: zero slash amount",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, "zero slash amount")
		return false
	}

	totalPower, totalTokens, consAddr, err := k.slashableStake(ctx, providerAddr)
	if err != nil {
		k.Logger(ctx).Info(
			"dropping matured downtime slash",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
			"reason", err,
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, err.Error())
		return false
	}

	if totalTokens.IsZero() {
		k.Logger(ctx).Info(
			"dropping matured downtime slash: validator has no slashable stake",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, "validator has no slashable stake")
		return false
	}

	fraction := math.LegacyNewDecFromInt(entry.SlashTokens).Quo(math.LegacyNewDecFromInt(totalTokens))
	if fraction.GT(ip.Downtime.SlashFraction) {
		fraction = ip.Downtime.SlashFraction
	}

	if _, err := k.stakingKeeper.SlashWithInfractionReason(ctx, consAddr, 0, totalPower, fraction, stakingtypes.Infraction_INFRACTION_DOWNTIME); err != nil {
		k.Logger(ctx).Error(
			"failed to execute matured downtime slash",
			"error", err,
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, err.Error())
		return false
	}

	k.Logger(ctx).Info(
		"executed matured downtime slash",
		"consumerId", consumerId,
		"providerAddr", providerAddr.String(),
		"fraction", fraction.String(),
		"slashTokens", entry.SlashTokens.String(),
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeExecuteConsumerChainSlash,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
			sdk.NewAttribute(vaastypes.AttributeProviderValidatorAddress, providerAddr.String()),
			sdk.NewAttribute(vaastypes.AttributeInfractionType, stakingtypes.Infraction_INFRACTION_DOWNTIME.String()),
			sdk.NewAttribute(vaastypes.AttributeSlashTokens, entry.SlashTokens.String()),
		),
	)

	return true
}

// emitDowntimeSlashDropped emits vaas_downtime_slash_dropped for a matured
// downtime slash entry that could not be executed (validator unbonded,
// tombstoned, vanished, or a zero-token entry), so the pending entry
// disappears with a visible reason instead of silently.
func (k Keeper) emitDowntimeSlashDropped(ctx sdk.Context, consumerId uint64, providerAddr types.ProviderConsAddress, reason string) {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeDowntimeSlashDropped,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
			sdk.NewAttribute(vaastypes.AttributeProviderValidatorAddress, providerAddr.String()),
			sdk.NewAttribute(vaastypes.AttributeDropReason, reason),
		),
	)
}
