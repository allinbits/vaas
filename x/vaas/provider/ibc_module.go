package provider

import (
	"bytes"
	"encoding/json"
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
	logger := im.keeper.Logger(ctx)

	if payload.DestinationPort != vaastypes.ProviderAppID {
		logger.Error("invalid destination port",
			"expected", vaastypes.ProviderAppID,
			"got", payload.DestinationPort,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	if payload.SourcePort != vaastypes.ConsumerAppID {
		logger.Error("invalid source port",
			"expected", vaastypes.ConsumerAppID,
			"got", payload.SourcePort,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	// destinationClient is the provider's own client pointing to the consumer.
	consumerId, found := im.keeper.GetClientIdToConsumerId(ctx, destinationClient)
	if !found {
		logger.Error("received packet from unknown client",
			"destinationClient", destinationClient,
			"sourceClient", sourceClient,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	var evidencePacket vaastypes.EvidencePacketData
	if err := json.Unmarshal(payload.Value, &evidencePacket); err != nil {
		logger.Error("cannot unmarshal evidence packet data", "error", err)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	if err := im.keeper.HandleConsumerEvidencePacket(ctx, consumerId, evidencePacket); err != nil {
		logger.Error("failed to handle evidence packet",
			"consumerId", consumerId,
			"error", err,
		)
		return channeltypesv2.RecvPacketResult{
			Status: channeltypesv2.PacketStatus_Failure,
		}
	}

	logger.Info("successfully handled evidence packet",
		"consumerId", consumerId,
		"sequence", sequence,
		"validator", evidencePacket.ValidatorAddr.String(),
		"infraction", evidencePacket.Infraction.String(),
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

	// Recover the acknowledged VSC id from the original payload so the keeper can
	// advance its acknowledged baseline. The provider only ever sends VSC packets,
	// so the payload is always ValidatorSetChangePacketData; on a decode failure
	// vscId stays 0 and the keeper simply will not advance the baseline.
	var vscId uint64
	var data vaastypes.ValidatorSetChangePacketData
	if err := vaastypes.ModuleCdc.UnmarshalJSON(payload.Value, &data); err == nil {
		vscId = data.ValsetUpdateId
	}

	if err := im.keeper.OnAcknowledgementPacketV2(ctx, sourceClient, vscId, ackError); err != nil {
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
