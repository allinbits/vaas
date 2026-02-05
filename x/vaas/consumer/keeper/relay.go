package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// OnRecvVSCPacket sets the pending validator set changes that will be flushed to ABCI on Endblock
// and set the maturity time for the packet.
//
// IBC v2 Note: Out-of-order packet handling is supported. If a packet with a lower valset_update_id
// than previously processed arrives, it is acknowledged but ignored. This allows safe handling of
// out-of-order delivery without strict channel ordering requirements.
func (k Keeper) OnRecvVSCPacket(ctx sdk.Context, packet channeltypes.Packet, newChanges vaastypes.ValidatorSetChangePacketData) error {
	// validate packet data upon receiving
	if err := newChanges.Validate(); err != nil {
		return errorsmod.Wrapf(err, "error validating VSCPacket data")
	}

	// IBC v2: Check for out-of-order packets
	// If this packet's valset_update_id is not higher than the highest we've seen,
	// acknowledge but don't process (stale/out-of-order packet)
	highestID := k.GetHighestValsetUpdateID(ctx)
	if newChanges.ValsetUpdateId <= highestID {
		k.Logger(ctx).Info("skipping out-of-order VSCPacket",
			"packetVscID", newChanges.ValsetUpdateId,
			"highestVscID", highestID,
		)
		// Return nil to acknowledge the packet without processing
		return nil
	}

	// get the provider channel
	providerChannel, found := k.GetProviderChannel(ctx)
	if found && providerChannel != packet.DestinationChannel {
		// VSC packet was sent on a channel different than the provider channel;
		// this should never happen
		panic(fmt.Errorf("VSCPacket received on unknown channel %s; expected: %s",
			packet.DestinationChannel, providerChannel))
	}
	if !found {
		// the first packet from the provider chain
		// - mark the CCV channel as established
		k.SetProviderChannel(ctx, packet.DestinationChannel)
		k.Logger(ctx).Info("CCV channel established", "port", packet.DestinationPort, "channel", packet.DestinationChannel)

		// emit event on first VSC packet to signal that CCV is working
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeChannelEstablished,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute(channeltypes.AttributeKeyChannelID, packet.DestinationChannel),
				sdk.NewAttribute(channeltypes.AttributeKeyPortID, packet.DestinationPort),
			),
		)
	}
	// Set pending changes by accumulating changes from this packet with all prior changes
	currentValUpdates := []abci.ValidatorUpdate{}
	currentChanges, exists := k.GetPendingChanges(ctx)
	if exists {
		currentValUpdates = currentChanges.ValidatorUpdates
	}
	pendingChanges := vaastypes.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)

	k.SetPendingChanges(ctx, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: pendingChanges,
	})

	// set height to VSC id mapping
	blockHeight := uint64(ctx.BlockHeight()) + 1
	k.SetHeightValsetUpdateID(ctx, blockHeight, newChanges.ValsetUpdateId)
	k.Logger(ctx).Debug("block height was mapped to vscID", "height", blockHeight, "vscID", newChanges.ValsetUpdateId)

	// IBC v2: Update the highest valset update ID for out-of-order packet tracking
	k.SetHighestValsetUpdateID(ctx, newChanges.ValsetUpdateId)

	// Note: Slash acks processing removed as slash functionality is not supported

	k.Logger(ctx).Info("finished receiving/handling VSCPacket",
		"vscID", newChanges.ValsetUpdateId,
		"len updates", len(newChanges.ValidatorUpdates),
	)
	return nil
}

// OnRecvVSCPacketV2 handles VSC packets received via IBC v2 client-based routing.
// Unlike OnRecvVSCPacket, this version uses client ID for provider validation instead of
// channel ID, and doesn't establish a provider channel (channels don't exist in v2).
//
// IBC v2 Note: In IBC v2, packets are routed via client IDs and application IDs.
// The provider is identified by its client ID, not by a channel. Out-of-order packet
// handling remains the same as v1.
func (k Keeper) OnRecvVSCPacketV2(ctx sdk.Context, sourceClientID string, newChanges vaastypes.ValidatorSetChangePacketData) error {
	// validate packet data upon receiving
	if err := newChanges.Validate(); err != nil {
		return errorsmod.Wrapf(err, "error validating VSCPacket data")
	}

	// IBC v2: Check for out-of-order packets
	highestID := k.GetHighestValsetUpdateID(ctx)
	if newChanges.ValsetUpdateId <= highestID {
		k.Logger(ctx).Info("skipping out-of-order VSCPacket (v2)",
			"packetVscID", newChanges.ValsetUpdateId,
			"highestVscID", highestID,
			"sourceClientID", sourceClientID,
		)
		return nil
	}

	// IBC v2: Verify the source client is the provider
	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		// First packet from provider - set the provider client ID
		k.SetProviderClientID(ctx, sourceClientID)
		k.Logger(ctx).Info("Provider client established (v2)", "clientID", sourceClientID)

		// emit event on first VSC packet to signal that CCV is working
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypeChannelEstablished,
				sdk.NewAttribute(sdk.AttributeKeyModule, types.ModuleName),
				sdk.NewAttribute("client_id", sourceClientID),
				sdk.NewAttribute("routing_mode", "ibc_v2"),
			),
		)
	} else if providerClientID != sourceClientID {
		// VSC packet from unexpected client
		k.Logger(ctx).Error("VSCPacket received from unexpected client (v2)",
			"expectedClientID", providerClientID,
			"receivedClientID", sourceClientID,
		)
		return errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"VSCPacket from unexpected client %s; expected: %s", sourceClientID, providerClientID)
	}

	// Set pending changes by accumulating changes from this packet with all prior changes
	currentValUpdates := []abci.ValidatorUpdate{}
	currentChanges, exists := k.GetPendingChanges(ctx)
	if exists {
		currentValUpdates = currentChanges.ValidatorUpdates
	}
	pendingChanges := vaastypes.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)

	k.SetPendingChanges(ctx, vaastypes.ValidatorSetChangePacketData{
		ValidatorUpdates: pendingChanges,
	})

	// set height to VSC id mapping
	blockHeight := uint64(ctx.BlockHeight()) + 1
	k.SetHeightValsetUpdateID(ctx, blockHeight, newChanges.ValsetUpdateId)
	k.Logger(ctx).Debug("block height was mapped to vscID (v2)", "height", blockHeight, "vscID", newChanges.ValsetUpdateId)

	// Update the highest valset update ID
	k.SetHighestValsetUpdateID(ctx, newChanges.ValsetUpdateId)

	k.Logger(ctx).Info("finished receiving/handling VSCPacket (v2)",
		"vscID", newChanges.ValsetUpdateId,
		"len updates", len(newChanges.ValidatorUpdates),
		"sourceClientID", sourceClientID,
	)
	return nil
}

// Note: SendPackets removed - consumer does not send any packets to provider in vaas
// (slash packets removed, VSCMatured packets removed)

// OnAcknowledgementPacket executes application logic for acknowledgments of sent VSCMatured packets
// in conjunction with the ibc module's execution of "acknowledgePacket",
// according to https://github.com/cosmos/ibc/tree/main/spec/core/ics-004-channel-and-packet-semantics#processing-acknowledgements
func (k Keeper) OnAcknowledgementPacket(ctx sdk.Context, packet channeltypes.Packet, ack channeltypes.Acknowledgement) error {
	if res := ack.GetResult(); res != nil {
		// VSCMatured packets are popped from the consumer pending packets queue on send.
		// Nothing more to do here.
		return nil
	}

	if err := ack.GetError(); err != "" {
		// Reasons for ErrorAcknowledgment
		//  - packet data could not be successfully decoded
		// This should never happen.
		k.Logger(ctx).Error(
			"recv ErrorAcknowledgement",
			"channel", packet.SourceChannel,
			"error", err,
		)
		// Initiate ChanCloseInit using packet source (non-counterparty) port and channel
		err := k.ChanCloseInit(ctx, packet.SourcePort, packet.SourceChannel)
		if err != nil {
			return fmt.Errorf("ChanCloseInit(%s) failed: %s", packet.SourceChannel, err.Error())
		}
		// check if there is an established CCV channel to provider
		channelID, found := k.GetProviderChannel(ctx)
		if !found {
			return errorsmod.Wrapf(types.ErrNoProposerChannelId, "recv ErrorAcknowledgement on non-established channel %s", packet.SourceChannel)
		}
		if channelID != packet.SourceChannel {
			// Close the established CCV channel as well
			return k.ChanCloseInit(ctx, vaastypes.ConsumerPortID, channelID)
		}
	}
	return nil
}

// IsChannelClosed returns a boolean whether a given channel is in the CLOSED state
func (k Keeper) IsChannelClosed(ctx sdk.Context, channelID string) bool {
	channel, found := k.channelKeeper.GetChannel(ctx, vaastypes.ConsumerPortID, channelID)
	if !found || channel.State == channeltypes.CLOSED {
		return true
	}
	return false
}
