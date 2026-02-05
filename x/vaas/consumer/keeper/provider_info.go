package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	ibctm "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint" //nolint:golint

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetProviderInfo returns provider information using IBC v1 channel-based routing.
//
// Deprecated: This function uses IBC v1 channel-based routing. For IBC v2
// client-based routing, use GetProviderInfoV2 instead.
func (k Keeper) GetProviderInfo(ctx sdk.Context) (*types.QueryProviderInfoResponse, error) { //nolint:golint
	consumerChannelID, found := k.GetProviderChannel(ctx)
	if !found {
		return nil, vaastypes.ErrChannelNotFound
	}
	consumerChannel, found := k.channelKeeper.GetChannel(ctx, vaastypes.ConsumerPortID, consumerChannelID)
	if !found {
		return nil, vaastypes.ErrChannelNotFound
	}

	// from channel get connection
	consumerConnectionID, consumerConnection, err := k.channelKeeper.GetChannelConnection(ctx, vaastypes.ConsumerPortID, consumerChannelID)
	if err != nil {
		return nil, err
	}

	providerChannelID := consumerChannel.Counterparty.ChannelId
	providerConnection := consumerConnection.Counterparty

	consumerClientState, found := k.clientKeeper.GetClientState(ctx, consumerConnection.ClientId)
	if !found {
		return nil, vaastypes.ErrClientNotFound
	}
	providerChainID := consumerClientState.(*ibctm.ClientState).ChainId

	resp := types.QueryProviderInfoResponse{
		Consumer: types.ChainInfo{
			ChainID:      ctx.ChainID(),
			ClientID:     consumerConnection.ClientId,
			ConnectionID: consumerConnectionID,
			ChannelID:    consumerChannelID,
		},

		Provider: types.ChainInfo{
			ChainID:      providerChainID,
			ClientID:     providerConnection.ClientId,
			ConnectionID: providerConnection.ConnectionId,
			ChannelID:    providerChannelID,
		},
	}

	return &resp, nil
}

// GetProviderInfoV2 returns provider information using IBC v2 client-based routing.
//
// IBC v2 Note: In IBC v2, routing is done via client IDs instead of channels.
// This function returns provider info based on the established provider client ID.
// Connection and channel information is not available in v2 as these concepts
// don't exist in the Eureka model.
func (k Keeper) GetProviderInfoV2(ctx sdk.Context) (*types.QueryProviderInfoResponse, error) {
	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		return nil, vaastypes.ErrClientNotFound
	}

	// Get the provider's client state to extract chain ID
	providerClientState, found := k.clientKeeper.GetClientState(ctx, providerClientID)
	if !found {
		return nil, vaastypes.ErrClientNotFound
	}
	providerChainID := providerClientState.(*ibctm.ClientState).ChainId

	// In IBC v2, we don't have connection/channel info - only client-based routing
	resp := types.QueryProviderInfoResponse{
		Consumer: types.ChainInfo{
			ChainID:  ctx.ChainID(),
			ClientID: providerClientID, // The client ID we use to talk to provider
			// ConnectionID and ChannelID are empty in v2
		},

		Provider: types.ChainInfo{
			ChainID: providerChainID,
			// In v2, we don't have direct access to the provider's client ID for us
			// The provider tracks us by our client ID on their side
		},
	}

	return &resp, nil
}
