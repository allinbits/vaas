package consumer

import (
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/consumer/keeper"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// IBCModuleV2 implements the IBC v2 (Eureka) module interface for the consumer.
// This module handles packet callbacks using client-based routing instead of
// channel-based routing.
//
// IBC v2 Note: In IBC v2, there are no channel handshake callbacks. Modules are
// registered with application IDs (e.g., "vaas/consumer") and packets are routed
// directly via client IDs. The consumer receives VSC packets from the provider
// to update its validator set.
type IBCModuleV2 struct {
	keeper *keeper.Keeper
}

// NewIBCModuleV2 creates a new IBC v2 module for the consumer.
func NewIBCModuleV2(k *keeper.Keeper) IBCModuleV2 {
	return IBCModuleV2{keeper: k}
}

// OnSendPacket is called when the consumer sends a packet.
// In VAAS, the consumer does not send packets to the provider
// (slash packets and VSCMatured packets have been removed).
func (im IBCModuleV2) OnSendPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	signer sdk.AccAddress,
) error {
	// Consumer does not send packets in VAAS
	im.keeper.Logger(ctx).Error("consumer attempted to send packet (v2)",
		"sourceClient", sourceClient,
		"destinationClient", destinationClient,
		"sequence", sequence,
	)

	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "consumer does not send packets")
}

// OnRecvPacket handles incoming VSC packets from the provider.
// This is the main packet handler for the consumer, receiving validator set updates.
//
// IBC v2 Note: Packets are received via client-based routing. The source client ID
// is used to verify the packet is from the expected provider. Out-of-order packets
// are handled gracefully (acknowledged but ignored if stale).
func (im IBCModuleV2) OnRecvPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) channeltypesv2.RecvPacketResult {
	logger := im.keeper.Logger(ctx)

	// Validate the payload is for the correct application
	if payload.DestinationPort != vaastypes.ConsumerAppID {
		logger.Error("invalid destination port (v2)",
			"expected", vaastypes.ConsumerAppID,
			"got", payload.DestinationPort,
		)
		return channeltypesv2.RecvPacketResult{
			Status:          channeltypesv2.PacketStatus_Failure,
			Acknowledgement: vaastypes.NewErrorAcknowledgementWithLog(ctx, errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "invalid destination port: expected %s, got %s", vaastypes.ConsumerAppID, payload.DestinationPort)).Acknowledgement(),
		}
	}

	// Unmarshal the VSC packet data
	var data vaastypes.ValidatorSetChangePacketData
	if err := vaastypes.ModuleCdc.UnmarshalJSON(payload.Value, &data); err != nil {
		ackErr := errorsmod.Wrapf(sdkerrors.ErrInvalidType, "cannot unmarshal VSCPacket data")
		logger.Error(fmt.Sprintf("%s sequence %d (v2)", ackErr.Error(), sequence))
		return channeltypesv2.RecvPacketResult{
			Status:          channeltypesv2.PacketStatus_Failure,
			Acknowledgement: vaastypes.NewErrorAcknowledgementWithLog(ctx, ackErr).Acknowledgement(),
		}
	}

	// Process the VSC packet using v2 handler
	if err := im.keeper.OnRecvVSCPacketV2(ctx, sourceClient, data); err != nil {
		logger.Error(fmt.Sprintf("%s sequence %d (v2)", err.Error(), sequence))
		return channeltypesv2.RecvPacketResult{
			Status:          channeltypesv2.PacketStatus_Failure,
			Acknowledgement: vaastypes.NewErrorAcknowledgementWithLog(ctx, err).Acknowledgement(),
		}
	}

	logger.Info("successfully handled VSCPacket (v2)",
		"sequence", sequence,
		"sourceClient", sourceClient,
		"vscID", data.ValsetUpdateId,
	)

	// Emit event
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePacket,
			sdk.NewAttribute(sdk.AttributeKeyModule, vaastypes.ModuleName),
			sdk.NewAttribute(vaastypes.AttributeValSetUpdateID, strconv.Itoa(int(data.ValsetUpdateId))),
			sdk.NewAttribute(vaastypes.AttributeKeyAckSuccess, "true"),
			sdk.NewAttribute("routing_mode", "ibc_v2"),
			sdk.NewAttribute("source_client", sourceClient),
		),
	)

	// Return success acknowledgement
	return channeltypesv2.RecvPacketResult{
		Status:          channeltypesv2.PacketStatus_Success,
		Acknowledgement: []byte{byte(1)}, // Success byte
	}
}

// OnTimeoutPacket handles packet timeouts.
// Since the consumer does not send packets in VAAS, this should not be called.
func (im IBCModuleV2) OnTimeoutPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
	// Consumer does not send packets, so timeouts should not occur
	im.keeper.Logger(ctx).Error("unexpected timeout on consumer (v2)",
		"sourceClient", sourceClient,
		"sequence", sequence,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeTimeout,
			sdk.NewAttribute(sdk.AttributeKeyModule, consumertypes.ModuleName),
			sdk.NewAttribute("source_client", sourceClient),
			sdk.NewAttribute("sequence", strconv.FormatUint(sequence, 10)),
		),
	)

	return nil
}

// OnAcknowledgementPacket handles acknowledgements for sent packets.
// Since the consumer does not send packets in VAAS, this should not be called.
func (im IBCModuleV2) OnAcknowledgementPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	acknowledgement []byte,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
	// Consumer does not send packets, so acks should not occur
	im.keeper.Logger(ctx).Error("unexpected acknowledgement on consumer (v2)",
		"sourceClient", sourceClient,
		"sequence", sequence,
	)

	return nil
}
