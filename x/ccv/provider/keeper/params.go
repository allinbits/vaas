package keeper

import (
	"fmt"
	"time"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/allinbits/vaas/x/ccv/provider/types"
)

// GetTemplateClient returns the template consumer client
func (k Keeper) GetTemplateClient(ctx sdk.Context) *ibctmtypes.ClientState {
	params := k.GetParams(ctx)
	return params.TemplateClient
}

// GetTrustingPeriodFraction returns a TrustingPeriodFraction
// used to compute the provider IBC client's TrustingPeriod as UnbondingPeriod / TrustingPeriodFraction
func (k Keeper) GetTrustingPeriodFraction(ctx sdk.Context) string {
	params := k.GetParams(ctx)
	return params.TrustingPeriodFraction
}

// GetCCVTimeoutPeriod returns the timeout period for sent ibc packets
func (k Keeper) GetCCVTimeoutPeriod(ctx sdk.Context) time.Duration {
	params := k.GetParams(ctx)
	return params.CcvTimeoutPeriod
}

// GetBlocksPerEpoch returns the number of blocks that constitute an epoch
func (k Keeper) GetBlocksPerEpoch(ctx sdk.Context) int64 {
	params := k.GetParams(ctx)
	return params.BlocksPerEpoch
}

// GetMaxProviderConsensusValidators returns the number of validators that will be passed on from the staking module
// to the consensus engine on the provider
func (k Keeper) GetMaxProviderConsensusValidators(ctx sdk.Context) int64 {
	params := k.GetParams(ctx)
	return params.MaxProviderConsensusValidators
}

// GetParams returns the paramset for the provider module
func (k Keeper) GetParams(ctx sdk.Context) types.Params {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ParametersKey())
	var params types.Params
	err := k.cdc.Unmarshal(bz, &params)
	if err != nil {
		panic(fmt.Sprintf("error unmarshalling module parameters: %v:", err))
	}
	return params
}

// SetParams sets the params for the provider module
func (k Keeper) SetParams(ctx sdk.Context, params types.Params) {
	store := ctx.KVStore(k.storeKey)
	bz := k.cdc.MustMarshal(&params)
	store.Set(types.ParametersKey(), bz)
}
