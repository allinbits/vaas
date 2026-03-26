package provider

import (
	"bytes"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
	"github.com/cosmos/ibc-go/v10/modules/core/api"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// Ensure IBCModuleV2 implements the IBC v2 module interface.
var _ api.IBCModule = (*IBCModuleV2)(nil)

// IBCModuleV2 implements the IBC v2 (Eureka) module interface for the provider.
// This module handles packet callbacks using client-based routing instead of
// channel-based routing.
//
// IBC v2 Note: In IBC v2, there are no channel handshake callbacks. Modules are
// registered with application IDs (e.g., "vaas/provider") and packets are routed
// directly via client IDs.
type IBCModuleV2 struct {
	keeper *keeper.Keeper
}

// NewIBCModuleV2 creates a new IBC v2 module for the provider.
func NewIBCModuleV2(k *keeper.Keeper) IBCModuleV2 {
	return IBCModuleV2{keeper: k}
}

// OnSendPacket is called when the provider sends a packet.
// The provider sends VSC packets to consumers.
func (im IBCModuleV2) OnSendPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	signer sdk.AccAddress,
) error {
	// Validate the payload is for the correct application
	if payload.SourcePort != vaastypes.ProviderAppID {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"invalid source port: expected %s, got %s", vaastypes.ProviderAppID, payload.SourcePort)
	}
	if payload.DestinationPort != vaastypes.ConsumerAppID {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"invalid destination port: expected %s, got %s", vaastypes.ConsumerAppID, payload.DestinationPort)
	}
	if signer.String() != im.keeper.GetAuthority() {
		return errorsmod.Wrapf(
			sdkerrors.ErrUnauthorized,
			"signer %s is different from authority %s",
			signer.String(),
			im.keeper.GetAuthority(),
		)
	}

	im.keeper.Logger(ctx).Debug("OnSendPacket (v2)",
		"sourceClient", sourceClient,
		"destinationClient", destinationClient,
		"sequence", sequence,
	)

	return nil
}

// OnRecvPacket handles incoming packets for the provider.
// The provider does not expect to receive any packets from consumers in VAAS.
func (im IBCModuleV2) OnRecvPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) channeltypesv2.RecvPacketResult {
	// Provider does not accept packets from consumers
	im.keeper.Logger(ctx).Error("provider received unexpected packet (v2)",
		"sourceClient", sourceClient,
		"destinationClient", destinationClient,
		"sequence", sequence,
	)

	return channeltypesv2.RecvPacketResult{
		Status:          channeltypesv2.PacketStatus_Failure,
		Acknowledgement: vaastypes.NewErrorAcknowledgementWithLog(ctx, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "provider does not accept packets")).Acknowledgement(),
	}
}

// OnTimeoutPacket handles packet timeouts.
// A timeout triggers immediate consumer removal as per the IBC v2 spec.
func (im IBCModuleV2) OnTimeoutPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
	// Use the v2 timeout handler
	if err := im.keeper.OnTimeoutPacketV2(ctx, sourceClient); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeTimeout,
			sdk.NewAttribute(sdk.AttributeKeyModule, providertypes.ModuleName),
			sdk.NewAttribute("source_client", sourceClient),
			sdk.NewAttribute("sequence", strconv.FormatUint(sequence, 10)),
		),
	)

	return nil
}

// OnAcknowledgementPacket handles acknowledgements for sent packets.
func (im IBCModuleV2) OnAcknowledgementPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	acknowledgement []byte,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
	ackError := ""
	if bytes.Equal(acknowledgement, channeltypesv2.ErrorAcknowledgement[:]) {
		ackError = "error acknowledgement received"
	}

	if err := im.keeper.OnAcknowledgementPacketV2(ctx, sourceClient, ackError); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePacket,
			sdk.NewAttribute(sdk.AttributeKeyModule, providertypes.ModuleName),
			sdk.NewAttribute("source_client", sourceClient),
			sdk.NewAttribute("sequence", strconv.FormatUint(sequence, 10)),
		),
	)

	return nil
}
