package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) OnRecvVSCPacketV2(ctx sdk.Context, sourceClientID string, newChanges vaastypes.ValidatorSetChangePacketData) error {
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
			"sourceClientID", sourceClientID,
		)
		return nil
	}

	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		k.SetProviderClientID(ctx, sourceClientID)
		k.Logger(ctx).Info("Provider client established", "clientID", sourceClientID)

		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeChannelEstablished,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute("client_id", sourceClientID),
			),
		)
	} else if providerClientID != sourceClientID {
		k.Logger(ctx).Error("VSCPacket received from unexpected client",
			"expectedClientID", providerClientID,
			"receivedClientID", sourceClientID,
		)
		return errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"VSCPacket from unexpected client %s; expected: %s", sourceClientID, providerClientID)
	}

	currentValUpdates := []abci.ValidatorUpdate{}
	currentChanges, exists := k.GetPendingChanges(ctx)
	if exists {
		currentValUpdates = currentChanges.ValidatorUpdates
	}
	pendingChanges := vaastypes.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)

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
		"sourceClientID", sourceClientID,
	)
	return nil
}
