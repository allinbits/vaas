package types

import (
	"context"
	"time"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"

	channelkeeperv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/keeper"
	channeltypesv2 "github.com/cosmos/ibc-go/v10/modules/core/04-channel/v2/types"
)

// ChannelV2Adapter wraps *channelkeeperv2.Keeper and implements IBCPacketHandler
// by calling the public MsgServer.SendPacket method with a module account signer.
//
// This is necessary because ibc-go v10's packet commitment logic (sendPacket) is
// only accessible through the MsgServer. The provider module needs to send VSC packets
// from within EndBlock, so we construct and execute MsgSendPacket directly.
//
// NOTE: OnSendPacket callback on the IBCModule will be invoked by the MsgServer.
// The provider's OnSendPacket validates that the signer is the authority, so we use
// the gov module account address as the signer here.
type ChannelV2Adapter struct {
	keeper    *channelkeeperv2.Keeper
	authority string
}

// NewChannelV2Adapter creates a new adapter wrapping the given channel v2 keeper.
// authority is the module account address used as the signer for MsgSendPacket
// (must match the provider keeper's authority to pass OnSendPacket validation).
func NewChannelV2Adapter(keeper *channelkeeperv2.Keeper, authority string) *ChannelV2Adapter {
	return &ChannelV2Adapter{keeper: keeper, authority: authority}
}

// SendPacket sends an IBC v2 packet by constructing and executing MsgSendPacket.
//
// sourceClient is the client ID on the sending chain (e.g., "07-tendermint-0").
// destApp is the application identifier on the destination chain (e.g., "vaasconsumer").
// timeoutTimestamp is the absolute timeout in nanoseconds.
// data is the packet payload.
func (a *ChannelV2Adapter) SendPacket(
	ctx context.Context,
	sourceClient string,
	destApp string,
	timeoutTimestamp uint64,
	data []byte,
) (uint64, error) {
	payload := channeltypesv2.NewPayload(
		ProviderAppID,
		destApp,
		"vaas-v1",
		"application/x-protobuf",
		data,
	)

	msg := channeltypesv2.NewMsgSendPacket(
		sourceClient,
		timeoutTimestamp,
		a.authority,
		payload,
	)

	resp, err := a.keeper.SendPacket(ctx, msg)
	if err != nil {
		return 0, errorsmod.Wrap(err, "failed to send IBC v2 packet")
	}

	return resp.Sequence, nil
}

// SendPacketWithTimeout is a convenience method that computes the timeout timestamp
// from a duration and delegates to SendPacket.
func (a *ChannelV2Adapter) SendPacketWithTimeout(
	ctx context.Context,
	sourceClient string,
	destApp string,
	data []byte,
	timeoutPeriod time.Duration,
) (uint64, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	timeoutTimestamp := uint64(sdkCtx.BlockTime().Add(timeoutPeriod).UnixNano())
	return a.SendPacket(ctx, sourceClient, destApp, timeoutTimestamp, data)
}
