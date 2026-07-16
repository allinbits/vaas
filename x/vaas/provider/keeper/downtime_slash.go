package keeper

import (
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
func (k Keeper) SweepPendingDowntimeSlashes(ctx sdk.Context) {
	ip := k.GetInfractionParams(ctx)

	iter, err := k.PendingDowntimeSlashes.Iterate(ctx, nil)
	if err != nil {
		k.Logger(ctx).Error("failed to iterate pending downtime slashes", "error", err)
		return
	}

	var maturedKeys []collections.Pair[uint64, []byte]
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

	for i, key := range maturedKeys {
		k.executeDowntimeSlash(ctx, key, maturedEntries[i], ip)
	}

	for _, key := range maturedKeys {
		if err := k.PendingDowntimeSlashes.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete matured pending downtime slash", "error", err)
		}
	}

	k.PruneEpochShareRecords(ctx, ctx.BlockTime().Add(-(ip.DowntimeEvidenceMaxAge + ip.DowntimeChallengeWindow)))
}

// executeDowntimeSlash executes a single matured downtime slash entry. It
// converts entry.SlashTokens into a stake fraction against the validator's
// current slashable stake, capped by ip.Downtime.SlashFraction, and calls
// SlashWithInfractionReason -- never Jail. Entries whose validator has since
// unbonded, been tombstoned, or vanished are dropped with an event instead of
// slashed; so is an entry with a zero slash amount or a validator whose
// slashable stake computes to zero tokens. A staking error from
// SlashWithInfractionReason itself is also reported via the dropped event.
func (k Keeper) executeDowntimeSlash(ctx sdk.Context, key collections.Pair[uint64, []byte], entry types.PendingDowntimeSlash, ip types.InfractionParameters) {
	consumerId := key.K1()
	providerAddr := types.NewProviderConsAddress(sdk.ConsAddress(key.K2()))

	if entry.SlashTokens.IsZero() {
		k.Logger(ctx).Info(
			"dropping matured downtime slash: zero slash amount",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, "zero slash amount")
		return
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
		return
	}

	if totalTokens.IsZero() {
		k.Logger(ctx).Info(
			"dropping matured downtime slash: validator has no slashable stake",
			"consumerId", consumerId,
			"providerAddr", providerAddr.String(),
		)
		k.emitDowntimeSlashDropped(ctx, consumerId, providerAddr, "validator has no slashable stake")
		return
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
		return
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
