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

var _ api.IBCModule = (*IBCModule)(nil)

type IBCModule struct {
	keeper *keeper.Keeper
}

func NewIBCModule(k *keeper.Keeper) IBCModule {
	return IBCModule{keeper: k}
}

func (im IBCModule) OnSendPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	signer sdk.AccAddress,
) error {
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

	im.keeper.Logger(ctx).Debug("OnSendPacket",
		"sourceClient", sourceClient,
		"destinationClient", destinationClient,
		"sequence", sequence,
	)

	return nil
}

func (im IBCModule) OnRecvPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) channeltypesv2.RecvPacketResult {
	im.keeper.Logger(ctx).Error("provider received unexpected packet",
		"sourceClient", sourceClient,
		"destinationClient", destinationClient,
		"sequence", sequence,
	)

	return channeltypesv2.RecvPacketResult{
		Status: channeltypesv2.PacketStatus_Failure,
	}
}

func (im IBCModule) OnTimeoutPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
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

func (im IBCModule) OnAcknowledgementPacket(
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
