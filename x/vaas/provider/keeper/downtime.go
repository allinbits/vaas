package keeper

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

//
// Consumer-initiated slashing section
//

// HandleConsumerEvidencePacket handles an evidence packet received from a consumer chain.
// It dispatches to the appropriate handler based on the infraction type.
func (k Keeper) HandleConsumerEvidencePacket(ctx sdk.Context, consumerId uint64, evidencePacket vaastypes.EvidencePacketData) error {
	if err := evidencePacket.Validate(); err != nil {
		return errorsmod.Wrapf(vaastypes.ErrInvalidPacketData, "invalid evidence packet: %s", err)
	}

	if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidConsumerState,
			"consumer chain %d is not launched (phase: %s)",
			consumerId,
			k.GetConsumerPhase(ctx, consumerId),
		)
	}

	switch evidencePacket.Infraction {
	case stakingtypes.Infraction_INFRACTION_DOWNTIME:
		return k.HandleConsumerDowntime(ctx, consumerId, evidencePacket)
	default:
		return fmt.Errorf("unsupported infraction type in evidence packet: %s", evidencePacket.Infraction)
	}
}

// HandleConsumerDowntime validates a consumer's downtime evidence and, if
// accepted, queues a slash behind the downtime challenge window instead of
// executing it immediately. The provider verifies:
//  1. The echoed downtime params (window, min-signed fraction) are acceptable
//     -- they match the provider's current params, or a recently-superseded
//     set (see AcceptableDowntimeParams).
//  2. The reported missed-block count exceeds the infraction threshold
//     computed from those echoed params (EvidencePacketData.MaxMissed).
//  3. The window-end height is not older than the minimum evidence height
//     for this consumer, and the validator was part of the consumer's
//     validator set.
//  4. The window-end time -- anchored to an IBC consensus state proving the
//     consumer chain actually reached that height, see windowEndTimestamp --
//     is past the consumer's downtime grace period and not older than
//     DowntimeEvidenceMaxAge.
//  5. The window starts above the pair's pruned acceptance floor
//     (DowntimeWindowFloors) and does not intersect any window already
//     accepted for this (consumer, validator) pair
//     (AcceptedDowntimeWindows). Multiple disjoint windows may be pending
//     for the same pair at once, and acceptance is order-independent: the
//     delivery order of disjoint windows never affects which are accepted.
//
// On acceptance the slash amount is priced now, at receipt time (see
// ResolveDowntimeSlashTokens), and stored as a PendingDowntimeSlash maturing
// DowntimeChallengeWindow from now, keyed by (consumer, validator, window-end
// height) so it coexists with any other window still pending for the same
// pair. Execution happens later, once matured, in the BeginBlock sweep
// (SweepPendingDowntimeSlashes): this handler never calls SlashValidator.
//
// CONTRACT: A downtime infraction must never jail a validator.
func (k Keeper) HandleConsumerDowntime(ctx sdk.Context, consumerId uint64, evidencePacket vaastypes.EvidencePacketData) error {
	echoedParams := vaastypes.DowntimeParams{
		SignedBlocksWindow: evidencePacket.SignedBlocksWindow,
		MinSignedPerWindow: evidencePacket.MinSignedPerWindow,
	}
	if !k.AcceptableDowntimeParams(ctx, echoedParams) {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d echoes unacceptable downtime params (window %d, min signed %s)",
			consumerId,
			evidencePacket.SignedBlocksWindow,
			evidencePacket.MinSignedPerWindow,
		)
	}

	maxMissed := evidencePacket.MaxMissed()
	if evidencePacket.MissedCount() <= maxMissed {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d does not exceed the infraction threshold: missed %d, max %d",
			consumerId,
			evidencePacket.MissedCount(),
			maxMissed,
		)
	}

	consumerAddr := types.NewConsumerConsAddress(evidencePacket.ValidatorAddr)
	providerAddr := k.GetProviderAddrFromConsumerAddr(ctx, consumerId, consumerAddr)

	// Verify the window-end height is not too old.
	minHeight := k.GetEquivocationEvidenceMinHeight(ctx, consumerId)
	if uint64(evidencePacket.WindowEndHeight) < minHeight {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d is too old: window end height (%d), min (%d)",
			consumerId,
			evidencePacket.WindowEndHeight,
			minHeight,
		)
	}

	// Verify the validator was part of the consumer's validator set. Join-time
	// trimming of the bitmap happens consumer-side, so there is no join-height
	// comparison here.
	if _, found := k.GetConsumerValidator(ctx, consumerId, providerAddr); !found {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"validator %s is not in the validator set of consumer chain %d",
			providerAddr.String(),
			consumerId,
		)
	}

	clientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidConsumerState,
			"no IBC client found for consumer chain %d",
			consumerId,
		)
	}

	// Anchor the window-end time to a real IBC consensus state so the grace
	// and evidence-age checks below are judged against consumer chain time
	// the provider can actually verify, not the packet's own unverified claim.
	windowEndTime, err := k.windowEndTimestamp(ctx, clientId, evidencePacket.WindowEndHeight)
	if err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"cannot anchor downtime evidence window for consumer chain %d: %s",
			consumerId, err,
		)
	}

	// Check that the consumer chain is outside its downtime grace period.
	// During the grace period after launch, downtime evidence is suppressed to give
	// validators time to spin up their consumer chain nodes.
	infractionParams := k.GetInfractionParams(ctx)
	if infractionParams.DowntimeGracePeriod > 0 {
		initParams, err := k.GetConsumerInitializationParameters(ctx, consumerId)
		if err != nil {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidConsumerState,
				"cannot get initialization parameters for consumer chain %d: %s",
				consumerId, err,
			)
		}
		gracePeriodEnd := initParams.SpawnTime.Add(infractionParams.DowntimeGracePeriod)
		if windowEndTime.Before(gracePeriodEnd) {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"consumer chain %d is still in downtime grace period (launched %s, grace ends %s, window end %s)",
				consumerId,
				initParams.SpawnTime,
				gracePeriodEnd,
				windowEndTime,
			)
		}
	}

	// Verify the evidence isn't stale relative to when it was submitted.
	if age := ctx.BlockTime().Sub(windowEndTime); age > infractionParams.DowntimeEvidenceMaxAge {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"downtime evidence for consumer chain %d is too old: window ended %s ago, max age %s",
			consumerId,
			age,
			infractionParams.DowntimeEvidenceMaxAge,
		)
	}

	pairKey := collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes())

	// Reject evidence at or below the pair's pruned acceptance floor: the
	// AcceptedDowntimeWindows records covering those heights have been
	// pruned, so the intersection check below cannot vouch for them
	// (see PruneAcceptedDowntimeWindows for why nothing acceptable can sit
	// down there).
	floor, err := k.DowntimeWindowFloors.Get(ctx, pairKey)
	if err == nil {
		if evidencePacket.WindowStartHeight <= floor {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"downtime evidence for consumer chain %d is at or below the pruned acceptance floor for validator %s: window start height %d, floor %d",
				consumerId,
				providerAddr.String(),
				evidencePacket.WindowStartHeight,
				floor,
			)
		}
	} else if !errors.Is(err, collections.ErrNotFound) {
		return fmt.Errorf("checking downtime window floor for consumer chain %d: %w", consumerId, err)
	}

	// Reject evidence whose window intersects one already accepted for this
	// (consumer, validator) pair, even after the earlier pending slash has
	// matured, executed, and been removed from PendingDowntimeSlashes --
	// otherwise the same infraction could be re-submitted and slashed a
	// second time once its original pending entry is gone. Checking
	// intersection against every retained record, rather than against a
	// single boundary, keeps acceptance order-independent: IBC v2 delivery
	// is out of order and relaying is permissionless, so accepting a later
	// window first must not void earlier windows still in flight. Accepted
	// windows for a pair are pairwise disjoint as a consequence.
	if err := k.checkAcceptedDowntimeWindowIntersection(
		ctx, consumerId, providerAddr, evidencePacket.WindowStartHeight, evidencePacket.WindowEndHeight,
	); err != nil {
		return err
	}

	slashTokens, err := k.ResolveDowntimeSlashTokens(ctx, consumerId, evidencePacket, windowEndTime)
	if err != nil {
		return errorsmod.Wrapf(
			vaastypes.ErrInvalidPacketData,
			"pricing downtime slash for consumer chain %d: %s",
			consumerId, err,
		)
	}

	// Record downtime for epoch reward exclusion. This takes effect
	// immediately, independent of the slash challenge window below.
	k.MarkEpochDowntime(ctx, consumerId, providerAddr.ToSdkConsAddr())

	maturesAt := ctx.BlockTime().Add(infractionParams.DowntimeChallengeWindow)
	pending := types.PendingDowntimeSlash{
		ConsumerId:         consumerId,
		ProviderConsAddr:   providerAddr.ToSdkConsAddr().Bytes(),
		WindowStartHeight:  evidencePacket.WindowStartHeight,
		Span:               evidencePacket.Span(),
		MissedCount:        evidencePacket.MissedCount(),
		MissedBlocksBitmap: evidencePacket.MissedBlocksBitmap,
		SlashTokens:        slashTokens,
		MaturesAt:          maturesAt,
	}
	// Keyed by (consumer, validator, window-end height) so this window's
	// entry coexists with any other window still pending for the same pair.
	pendingKey := collections.Join3(consumerId, providerAddr.ToSdkConsAddr().Bytes(), evidencePacket.WindowEndHeight)
	if err := k.PendingDowntimeSlashes.Set(ctx, pendingKey, pending); err != nil {
		return fmt.Errorf("storing pending downtime slash for consumer chain %d: %w", consumerId, err)
	}
	if err := k.AcceptedDowntimeWindows.Set(ctx, pendingKey, types.AcceptedDowntimeWindow{
		WindowStart: evidencePacket.WindowStartHeight,
		AcceptedAt:  ctx.BlockTime(),
	}); err != nil {
		return fmt.Errorf("storing accepted downtime window for consumer chain %d: %w", consumerId, err)
	}

	k.Logger(ctx).Info(
		"queued consumer downtime slash",
		"consumerId", consumerId,
		"consumerAddr", consumerAddr.String(),
		"providerAddr", providerAddr.String(),
		"windowStartHeight", evidencePacket.WindowStartHeight,
		"windowEndHeight", evidencePacket.WindowEndHeight,
		"missedCount", evidencePacket.MissedCount(),
		"slashTokens", slashTokens.String(),
		"maturesAt", maturesAt,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePendingDowntimeSlash,
			sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, fmt.Sprintf("%d", consumerId)),
			sdk.NewAttribute(vaastypes.AttributeProviderValidatorAddress, providerAddr.String()),
			sdk.NewAttribute(vaastypes.AttributeWindowStartHeight, fmt.Sprintf("%d", evidencePacket.WindowStartHeight)),
			sdk.NewAttribute(vaastypes.AttributeWindowEndHeight, fmt.Sprintf("%d", evidencePacket.WindowEndHeight)),
			sdk.NewAttribute(vaastypes.AttributeMissedCount, fmt.Sprintf("%d", evidencePacket.MissedCount())),
			sdk.NewAttribute(vaastypes.AttributeMissedBlocksBitmap, hex.EncodeToString(evidencePacket.MissedBlocksBitmap)),
			sdk.NewAttribute(vaastypes.AttributeSlashTokens, slashTokens.String()),
			sdk.NewAttribute(vaastypes.AttributeMaturesAt, maturesAt.String()),
		),
	)

	return nil
}

// checkAcceptedDowntimeWindowIntersection rejects a downtime evidence window
// [newStart, newEnd] that intersects any AcceptedDowntimeWindows record
// retained for (consumerId, providerAddr). Two closed height ranges
// intersect iff newStart <= retainedEnd && retainedStart <= newEnd. The
// sub-range scanned is bounded by however many accepted windows are
// currently retained for this single validator on this single consumer.
func (k Keeper) checkAcceptedDowntimeWindowIntersection(
	ctx sdk.Context, consumerId uint64, providerAddr types.ProviderConsAddress, newStart, newEnd int64,
) error {
	iter, err := k.AcceptedDowntimeWindows.Iterate(
		ctx, collections.NewSuperPrefixedTripleRange[uint64, []byte, int64](consumerId, providerAddr.ToSdkConsAddr().Bytes()),
	)
	if err != nil {
		return fmt.Errorf("checking accepted downtime windows for consumer chain %d: %w", consumerId, err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			return fmt.Errorf("reading accepted downtime window for consumer chain %d: %w", consumerId, err)
		}
		retainedStart, retainedEnd := kv.Value.WindowStart, kv.Key.K3()
		if newStart <= retainedEnd && retainedStart <= newEnd {
			return errorsmod.Wrapf(
				vaastypes.ErrInvalidPacketData,
				"downtime evidence for consumer chain %d intersects a window already accepted for validator %s: window [%d, %d], accepted window [%d, %d]",
				consumerId,
				providerAddr.String(),
				newStart,
				newEnd,
				retainedStart,
				retainedEnd,
			)
		}
	}
	return nil
}

// windowEndTimestamp resolves the timestamp anchor for a downtime evidence
// window: the timestamp of the smallest IBC consensus state stored for
// clientId at a height >= windowEnd. This proves the consumer chain actually
// reached at least that height, so the grace-period and evidence-age checks
// in HandleConsumerDowntime are judged against verifiable consumer chain
// time rather than the packet's own unverified claims. Returns an error if
// no such consensus state is stored.
func (k Keeper) windowEndTimestamp(ctx sdk.Context, clientId string, windowEnd int64) (time.Time, error) {
	if k.windowEndTimestampFn != nil {
		return k.windowEndTimestampFn(ctx, clientId, windowEnd)
	}

	clientStore := k.clientKeeper.GetStoreProvider().ClientStore(ctx, clientId)

	var anchorHeight ibcexported.Height
	found := false
	ibctmtypes.IterateConsensusStateAscending(clientStore, func(height ibcexported.Height) bool {
		if int64(height.GetRevisionHeight()) >= windowEnd {
			anchorHeight = height
			found = true
			return true
		}
		return false
	})
	if !found {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time: no consensus state at height >= %d", windowEnd)
	}

	consensusState, ok := k.clientKeeper.GetClientConsensusState(ctx, clientId, anchorHeight)
	if !ok {
		return time.Time{}, fmt.Errorf("cannot anchor evidence window time: consensus state lookup failed for height %s", anchorHeight)
	}
	return time.Unix(0, int64(consensusState.GetTimestamp())), nil //nolint:staticcheck
}
