package provider

import (
	"github.com/allinbits/vaas/x/vaas/provider/keeper"
	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v10/modules/core/05-port/types"
	ibcexported "github.com/cosmos/ibc-go/v10/modules/core/exported"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// OnChanOpenInit implements the IBCModule interface
//
// See: https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/methods.md#ccv-pcf-coinit1
// Spec Tag: [CCV-PCF-COINIT.1]
func (am AppModule) OnChanOpenInit(
	ctx sdk.Context,
	order channeltypes.Order,
	connectionHops []string,
	portID string,
	channelID string,
	counterparty channeltypes.Counterparty,
	version string,
) (string, error) {
	return version, errorsmod.Wrap(vaastypes.ErrInvalidChannelFlow, "channel handshake must be initiated by consumer chain")
}

// OnChanOpenTry implements the IBCModule interface
//
// See: https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/methods.md#ccv-pcf-cotry1
// Spec tag: [CCV-PCF-COTRY.1]
func (am AppModule) OnChanOpenTry(
	ctx sdk.Context,
	order channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	counterparty channeltypes.Counterparty,
	counterpartyVersion string,
) (metadata string, err error) {
	// Validate parameters
	if err := validateVAASChannelParams(
		ctx, am.keeper, order, portID,
	); err != nil {
		return "", err
	}

	// ensure the counterparty port ID matches the expected consumer port ID
	if counterparty.PortId != vaastypes.ConsumerPortID {
		return "", errorsmod.Wrapf(porttypes.ErrInvalidPort,
			"invalid counterparty port: %s, expected %s", counterparty.PortId, vaastypes.ConsumerPortID)
	}

	// ensure the counter party version matches the expected version
	if counterpartyVersion != vaastypes.Version {
		return "", errorsmod.Wrapf(
			vaastypes.ErrInvalidVersion, "invalid counterparty version: got: %s, expected %s",
			counterpartyVersion, vaastypes.Version)
	}

	if err := am.keeper.VerifyConsumerChain(
		ctx, channelID, connectionHops,
	); err != nil {
		return "", err
	}

	md := vaastypes.HandshakeMetadata{
		Version: vaastypes.Version,
	}
	mdBz, err := (&md).Marshal()
	if err != nil {
		return "", errorsmod.Wrapf(vaastypes.ErrInvalidHandshakeMetadata,
			"error marshalling ibc-try metadata: %v", err)
	}
	return string(mdBz), nil
}

// validateVAASChannelParams validates a VAAS channel
func validateVAASChannelParams(
	ctx sdk.Context,
	keeper *keeper.Keeper,
	order channeltypes.Order,
	portID string,
) error {
	if order != channeltypes.ORDERED {
		return errorsmod.Wrapf(channeltypes.ErrInvalidChannelOrdering, "expected %s channel, got %s ", channeltypes.ORDERED, order)
	}

	// the port ID must match the port ID the VAAS module is bounded to
	boundPort := keeper.GetPort(ctx)
	if boundPort != portID {
		return errorsmod.Wrapf(porttypes.ErrInvalidPort, "invalid port: %s, expected %s", portID, boundPort)
	}
	return nil
}

// OnChanOpenAck implements the IBCModule interface
//
// See: https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/methods.md#ccv-pcf-coack1
// Spec tag: [CCV-PCF-COACK.1]
func (am AppModule) OnChanOpenAck(
	ctx sdk.Context,
	portID,
	channelID string,
	counterpartyChannelID string,
	counterpartyVersion string,
) error {
	return errorsmod.Wrap(vaastypes.ErrInvalidChannelFlow, "channel handshake must be initiated by consumer chain")
}

// OnChanOpenConfirm implements the IBCModule interface
//
// See: https://github.com/cosmos/ibc/blob/main/spec/app/ics-028-cross-chain-validation/methods.md#ccv-pcf-coconfirm1
// Spec tag: [CCV-PCF-COCONFIRM.1]
func (am AppModule) OnChanOpenConfirm(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	err := am.keeper.SetConsumerChain(ctx, channelID)
	if err != nil {
		return err
	}
	return nil
}

// OnChanCloseInit implements the IBCModule interface
func (am AppModule) OnChanCloseInit(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	// Disallow user-initiated channel closing for provider channels
	return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "user cannot close channel")
}

// OnChanCloseConfirm implements the IBCModule interface
func (am AppModule) OnChanCloseConfirm(
	ctx sdk.Context,
	portID,
	channelID string,
) error {
	return nil
}

// OnRecvPacket implements the IBCModule interface.
// The provider does not expect to receive any packets from consumers.
func (am AppModule) OnRecvPacket(
	ctx sdk.Context,
	_ string,
	packet channeltypes.Packet,
	_ sdk.AccAddress,
) ibcexported.Acknowledgement {
	return channeltypes.NewErrorAcknowledgement(
		errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "provider does not accept packets"),
	)
}

// OnAcknowledgementPacket implements the IBCModule interface
func (am AppModule) OnAcknowledgementPacket(
	ctx sdk.Context,
	_ string,
	packet channeltypes.Packet,
	acknowledgement []byte,
	_ sdk.AccAddress,
) error {
	var ack channeltypes.Acknowledgement
	if err := vaastypes.ModuleCdc.UnmarshalJSON(acknowledgement, &ack); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrUnknownRequest, "cannot unmarshal provider packet acknowledgement: %v", err)
	}

	if err := am.keeper.OnAcknowledgementPacket(ctx, packet, ack); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePacket,
			sdk.NewAttribute(sdk.AttributeKeyModule, providertypes.ModuleName),
			sdk.NewAttribute(vaastypes.AttributeKeyAck, ack.String()),
		),
	)

	switch resp := ack.Response.(type) {
	case *channeltypes.Acknowledgement_Result:
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypePacket,
				sdk.NewAttribute(vaastypes.AttributeKeyAckSuccess, string(resp.Result)),
			),
		)
	case *channeltypes.Acknowledgement_Error:
		ctx.EventManager().EmitEvent(
			sdk.NewEvent(
				vaastypes.EventTypePacket,
				sdk.NewAttribute(vaastypes.AttributeKeyAckError, resp.Error),
			),
		)
	}

	return nil
}

// OnTimeoutPacket implements the IBCModule interface
func (am AppModule) OnTimeoutPacket(
	ctx sdk.Context,
	_ string,
	packet channeltypes.Packet,
	_ sdk.AccAddress,
) error {
	if err := am.keeper.OnTimeoutPacket(ctx, packet); err != nil {
		return err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeTimeout,
			sdk.NewAttribute(sdk.AttributeKeyModule, providertypes.ModuleName),
		),
	)

	return nil
}
