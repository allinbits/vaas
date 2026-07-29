package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	ibctm "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint" //nolint:golint

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) GetProviderInfoV2(ctx sdk.Context) (*types.QueryProviderInfoResponse, error) {
	providerClientID, found := k.GetProviderClientID(ctx)
	if !found {
		return nil, vaastypes.ErrClientNotFound
	}

	providerClientState, found := k.clientKeeper.GetClientState(ctx, providerClientID)
	if !found {
		return nil, vaastypes.ErrClientNotFound
	}
	tmClientState, ok := providerClientState.(*ibctm.ClientState)
	if !ok {
		return nil, errorsmod.Wrapf(types.ErrInvalidProviderClient,
			"provider client %s is not a tendermint client", providerClientID)
	}
	providerChainID := tmClientState.ChainId

	resp := types.QueryProviderInfoResponse{
		Consumer: types.ChainInfo{
			ChainID:  ctx.ChainID(),
			ClientID: providerClientID,
		},
		Provider: types.ChainInfo{
			ChainID: providerChainID,
		},
	}

	return &resp, nil
}
