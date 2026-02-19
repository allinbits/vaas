package keeper

import (
	"context"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetTemplateClient returns the template consumer client
func (k Keeper) GetTemplateClient(ctx context.Context) *ibctmtypes.ClientState {
	params := k.GetParams(ctx)
	return params.TemplateClient
}

// GetTrustingPeriodFraction returns a TrustingPeriodFraction
// used to compute the provider IBC client's TrustingPeriod as UnbondingPeriod / TrustingPeriodFraction
func (k Keeper) GetTrustingPeriodFraction(ctx context.Context) string {
	params := k.GetParams(ctx)
	return params.TrustingPeriodFraction
}

// GetVAASTimeoutPeriod returns the timeout period for sent ibc packets
func (k Keeper) GetVAASTimeoutPeriod(ctx context.Context) time.Duration {
	params := k.GetParams(ctx)
	return params.VaasTimeoutPeriod
}

// GetBlocksPerEpoch returns the number of blocks that constitute an epoch
func (k Keeper) GetBlocksPerEpoch(ctx context.Context) int64 {
	params := k.GetParams(ctx)
	return params.BlocksPerEpoch
}

// GetMaxProviderConsensusValidators returns the number of validators that will be passed on from the staking module
// to the consensus engine on the provider
func (k Keeper) GetMaxProviderConsensusValidators(ctx context.Context) int64 {
	params := k.GetParams(ctx)
	return params.MaxProviderConsensusValidators
}

// GetFeesPerBlock returns the fees that each consumer chain must pay per block
func (k Keeper) GetFeesPerBlock(ctx context.Context) sdk.Coin {
	params := k.GetParams(ctx)
	return params.FeesPerBlock
}

// GetParams returns the paramset for the provider module
func (k Keeper) GetParams(ctx context.Context) types.Params {
	params, err := k.Params.Get(ctx)
	if err != nil {
		panic(fmt.Sprintf("error getting module parameters: %v", err))
	}
	return params
}

// SetParams sets the params for the provider module
func (k Keeper) SetParams(ctx context.Context, params types.Params) {
	if err := k.Params.Set(ctx, params); err != nil {
		panic(fmt.Sprintf("error setting module parameters: %v", err))
	}
}
