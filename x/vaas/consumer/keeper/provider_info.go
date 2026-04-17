package keeper

import (
	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	ibctm "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint" //nolint:golint

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
	providerChainID := providerClientState.(*ibctm.ClientState).ChainId

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
