package consumer

import (
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/consumer/keeper"
	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
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
	if payload.SourcePort != vaastypes.ConsumerAppID {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"invalid source port: expected %s, got %s", vaastypes.ConsumerAppID, payload.SourcePort)
	}
	if payload.DestinationPort != vaastypes.ProviderAppID {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest,
			"invalid destination port: expected %s, got %s", vaastypes.ProviderAppID, payload.DestinationPort)
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
	logger := im.keeper.Logger(ctx)

	if payload.DestinationPort != vaastypes.ConsumerAppID {
		logger.Error("invalid destination port",
			"expected", vaastypes.ConsumerAppID,
			"got", payload.DestinationPort,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	if payload.SourcePort != vaastypes.ProviderAppID {
		logger.Error("invalid source port",
			"expected", vaastypes.ProviderAppID,
			"got", payload.SourcePort,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	var data vaastypes.ValidatorSetChangePacketData
	if err := vaastypes.ModuleCdc.UnmarshalJSON(payload.Value, &data); err != nil {
		ackErr := errorsmod.Wrapf(sdkerrors.ErrInvalidType, "cannot unmarshal VSCPacket data")
		logger.Error(fmt.Sprintf("%s sequence %d", ackErr.Error(), sequence))
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	// destinationClient is the consumer's own client that received this
	// packet; ibc-go's RecvPacket handler already verified it has a
	// registered counterparty before this callback runs, so it is the
	// correct, currently-live client to address packets back to the provider.
	if err := im.keeper.OnRecvVSCPacketV2(ctx, destinationClient, data); err != nil {
		logger.Error(fmt.Sprintf("%s sequence %d", err.Error(), sequence))
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	logger.Info("successfully handled VSCPacket",
		"sequence", sequence,
		"sourceClient", sourceClient,
		"vscID", data.ValsetUpdateId,
	)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypePacket,
			sdk.NewAttribute(sdk.AttributeKeyModule, vaastypes.ModuleName),
			sdk.NewAttribute(vaastypes.AttributeValSetUpdateID, strconv.Itoa(int(data.ValsetUpdateId))),
			sdk.NewAttribute(vaastypes.AttributeKeyAckSuccess, "true"),
			sdk.NewAttribute("source_client", sourceClient),
		),
	)

	return channeltypesv2.RecvPacketResult{
		Status:          channeltypesv2.PacketStatus_Success,
		Acknowledgement: []byte{byte(1)},
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
	im.keeper.Logger(ctx).Error("unexpected timeout on consumer",
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

func (im IBCModule) OnAcknowledgementPacket(
	ctx sdk.Context,
	sourceClient string,
	destinationClient string,
	sequence uint64,
	acknowledgement []byte,
	payload channeltypesv2.Payload,
	relayer sdk.AccAddress,
) error {
	im.keeper.Logger(ctx).Error("unexpected acknowledgement on consumer",
		"sourceClient", sourceClient,
		"sequence", sequence,
	)

	return nil
}
