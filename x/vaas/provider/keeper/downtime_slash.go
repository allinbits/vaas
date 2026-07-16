package keeper

import (
	"errors"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
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
// own expiry via sweepExpiredWithheldFeeRecords below.
func (k Keeper) SweepPendingDowntimeSlashes(ctx sdk.Context) {
	ip := k.GetInfractionParams(ctx)

	iter, err := k.PendingDowntimeSlashes.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("failed to iterate pending downtime slashes", "error", err)
		return
	}

	var maturedKeys []collections.Triple[uint64, []byte, int64]
	var maturedEntries []types.PendingDowntimeSlash
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			k.Logger(ctx).Error("failed to read pending downtime slash entry", "error", err)
			continue
		}
		if kv.Value.MaturesAt.After(ctx.BlockTime()) {
			continue
		}
		maturedKeys = append(maturedKeys, kv.Key)
		maturedEntries = append(maturedEntries, kv.Value)
	}
	iter.Close()

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
		drained, err := k.pairHasNoPendingDowntimeSlash(ctx, pk.consumerId, addrBytes)
		if err != nil {
			k.Logger(ctx).Error("failed to check remaining pending downtime slashes for pair", "error", err)
			continue
		}
		if !drained {
			continue
		}
		if err := k.WithheldFeeRecords.Remove(ctx, collections.Join(pk.consumerId, addrBytes)); err != nil && !errors.Is(err, collections.ErrNotFound) {
			k.Logger(ctx).Error("failed to delete withheld fee record after last pending downtime slash executed", "error", err)
		}
	}

	k.PruneEpochShareRecords(ctx, ctx.BlockTime().Add(-(ip.DowntimeEvidenceMaxAge + ip.DowntimeChallengeWindow)))

	k.sweepExpiredWithheldFeeRecords(ctx)

	k.pruneAcceptedDowntimeWindows(ctx, ip)
}

// pruneAcceptedDowntimeWindows deletes AcceptedDowntimeWindows records whose
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
func (k Keeper) pruneAcceptedDowntimeWindows(ctx sdk.Context, ip types.InfractionParameters) {
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

// pairHasNoPendingDowntimeSlash reports whether (consumerId,
// providerConsAddr) has zero remaining entries in PendingDowntimeSlashes,
// across every window-end height.
func (k Keeper) pairHasNoPendingDowntimeSlash(ctx sdk.Context, consumerId uint64, providerConsAddr []byte) (bool, error) {
	iter, err := k.PendingDowntimeSlashes.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerConsAddr),
	)
	if err != nil {
		return false, err
	}
	defer iter.Close()
	return !iter.Valid(), nil
}

// sweepExpiredWithheldFeeRecords deletes withheld fee records whose challenge
// window has elapsed without a successful challenge. Nothing is transferred:
// the escrowed funds were never drawn from the consumer's fee pool in the
// first place, so deleting the record simply lets them stay there for good.
func (k Keeper) sweepExpiredWithheldFeeRecords(ctx sdk.Context) {
	iter, err := k.WithheldFeeRecords.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("failed to iterate withheld fee records", "error", err)
		return
	}

	var expiredKeys []collections.Pair[uint64, []byte]
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			k.Logger(ctx).Error("failed to read withheld fee record", "error", err)
			continue
		}
		if kv.Value.ExpiresAt.After(ctx.BlockTime()) {
			continue
		}
		expiredKeys = append(expiredKeys, kv.Key)
	}
	iter.Close()

	for _, key := range expiredKeys {
		if err := k.WithheldFeeRecords.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete expired withheld fee record", "error", err)
		}
	}
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
