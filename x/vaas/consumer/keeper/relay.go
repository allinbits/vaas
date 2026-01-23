package keeper

import (
	"fmt"

	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	abci "github.com/cometbft/cometbft/abci/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	ccv "github.com/allinbits/vaas/x/vaas/types"
)

// OnRecvVSCPacket sets the pending validator set changes that will be flushed to ABCI on Endblock
// and set the maturity time for the packet. Once the maturity time elapses, a VSCMatured packet is
// sent back to the provider chain.
//
// Note: CCV uses an ordered IBC channel, meaning VSC packet changes will be accumulated (and later
// processed by ApplyCCValidatorChanges) s.t. more recent val power changes overwrite older ones.
func (k Keeper) OnRecvVSCPacket(ctx sdk.Context, packet channeltypes.Packet, newChanges ccv.ValidatorSetChangePacketData) error {
	// validate packet data upon receiving
	if err := newChanges.Validate(); err != nil {
		return errorsmod.Wrapf(err, "error validating VSCPacket data")
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
				ccv.EventTypeChannelEstablished,
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
	pendingChanges := ccv.AccumulateChanges(currentValUpdates, newChanges.ValidatorUpdates)

	k.SetPendingChanges(ctx, ccv.ValidatorSetChangePacketData{
		ValidatorUpdates: pendingChanges,
	})

	// set height to VSC id mapping
	blockHeight := uint64(ctx.BlockHeight()) + 1
	k.SetHeightValsetUpdateID(ctx, blockHeight, newChanges.ValsetUpdateId)
	k.Logger(ctx).Debug("block height was mapped to vscID", "height", blockHeight, "vscID", newChanges.ValsetUpdateId)

	// Note: Slash acks processing removed as slash functionality is not supported

	k.Logger(ctx).Info("finished receiving/handling VSCPacket",
		"vscID", newChanges.ValsetUpdateId,
		"len updates", len(newChanges.ValidatorUpdates),
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
			return k.ChanCloseInit(ctx, ccv.ConsumerPortID, channelID)
		}
	}
	return nil
}

// IsChannelClosed returns a boolean whether a given channel is in the CLOSED state
func (k Keeper) IsChannelClosed(ctx sdk.Context, channelID string) bool {
	channel, found := k.channelKeeper.GetChannel(ctx, ccv.ConsumerPortID, channelID)
	if !found || channel.State == channeltypes.CLOSED {
		return true
	}
	return false
}
