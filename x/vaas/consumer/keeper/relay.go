package keeper

import (
	"strconv"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// OnRecvVSCPacketV2 handles a validator-set-change packet from the provider.
// consumerClientID is the consumer's own IBC v2 client that received this
// packet (i.e. packet.DestinationClient, guaranteed by ibc-go's RecvPacket to
// have a registered counterparty before our callback ever runs) -- the value
// SendEvidencePackets later needs to address packets back to the provider.
func (k Keeper) OnRecvVSCPacketV2(ctx sdk.Context, consumerClientID string, newChanges vaastypes.ValidatorSetChangePacketData) error {
	if err := newChanges.Validate(); err != nil {
		return errorsmod.Wrapf(err, "error validating VSCPacket data")
	}

	highestID, found, err := k.GetHighestValsetUpdateID(ctx)
	if err != nil {
		return errorsmod.Wrapf(err, "error getting highest valset update ID")
	}

	if found && newChanges.ValsetUpdateId <= highestID {
		k.Logger(ctx).Info("skipping out-of-order VSCPacket",
			"packetVscID", newChanges.ValsetUpdateId,
			"highestVscID", highestID,
			"consumerClientID", consumerClientID,
		)
		return nil
	}

	k.SetLastVSCRecvTime(ctx, ctx.BlockTime())

	// Keep the stored client in sync with whichever client is actually
	// delivering VSC packets, rather than latching onto the first value ever
	// seen (which, at genesis, is a placeholder client the consumer created
	// for itself before the relayer established the real, counterparty-linked
	// client).
	if current, found := k.GetProviderClientID(ctx); !found || current != consumerClientID {
		k.SetProviderClientID(ctx, consumerClientID)
		k.Logger(ctx).Info("Provider client established", "clientID", consumerClientID)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeChannelEstablished,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute("client_id", consumerClientID),
			),
		)
	}

	k.SetConsumerInDebt(ctx, newChanges.ConsumerInDebt)

	// Set pending changes: snapshot packets replace the set; diff packets accumulate.
	var pendingChanges []abci.ValidatorUpdate
	if newChanges.IsSnapshot {
		pendingChanges = k.computeReplaceUpdates(ctx, newChanges.ValidatorUpdates)
		// Surface snapshot resyncs (not ordinary diffs) so operators -- and the
		// e2e -- can observe that a behind consumer was healed by a full-set
		// replacement rather than an accumulated diff. Emitted both as an event
		// (structured/queryable) and a log line (the e2e asserts on the log).
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeSnapshotResync,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(vaastypes.AttributeValSetUpdateID, strconv.FormatUint(newChanges.ValsetUpdateId, 10)),
				sdk.NewAttribute(vaastypes.AttributeNumValidators, strconv.Itoa(len(newChanges.ValidatorUpdates))),
			),
		)
		k.Logger(ctx).Info("applied snapshot resync",
			"vscID", newChanges.ValsetUpdateId,
			"numValidators", len(newChanges.ValidatorUpdates),
		)
	} else {
		currentValUpdates := []abci.ValidatorUpdate{}
		if currentChanges, exists := k.GetPendingChanges(ctx); exists {
			currentValUpdates = currentChanges.ValidatorUpdates
		}
		pendingChanges = vaastypes.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)
	}

	k.SetPendingChanges(ctx, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: pendingChanges,
	})

	blockHeight := uint64(ctx.BlockHeight()) + 1
	k.SetHeightValsetUpdateID(ctx, blockHeight, newChanges.ValsetUpdateId)
	k.Logger(ctx).Debug("block height was mapped to vscID", "height", blockHeight, "vscID", newChanges.ValsetUpdateId)

	if err := k.SetHighestValsetUpdateID(ctx, newChanges.ValsetUpdateId); err != nil {
		return errorsmod.Wrapf(err, "error setting highest valset update ID")
	}

	k.Logger(ctx).Info("finished receiving/handling VSCPacket",
		"vscID", newChanges.ValsetUpdateId,
		"len updates", len(newChanges.ValidatorUpdates),
		"consumerClientID", consumerClientID,
	)
	return nil
}
