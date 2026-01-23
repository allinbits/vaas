package keeper

import (
	"context"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	ccvtypes "github.com/allinbits/vaas/x/vaas/types"
)

// GetParams returns the params for the consumer ccv module
// NOTE: it is different from the GetParams method which is required to implement StakingKeeper interface
func (k Keeper) GetConsumerParams(ctx sdk.Context) ccvtypes.ConsumerParams {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ParametersKey())
	var params ccvtypes.ConsumerParams
	k.cdc.MustUnmarshal(bz, &params)
	return params
}

// SetParams sets the paramset for the consumer module
func (k Keeper) SetParams(ctx sdk.Context, params ccvtypes.ConsumerParams) {
	store := ctx.KVStore(k.storeKey)
	bz := k.cdc.MustMarshal(&params)
	store.Set(types.ParametersKey(), bz)
}

// GetParams implements StakingKeeper GetParams interface method
// it returns an a empty stakingtypes.Params struct
// NOTE: this method must be implemented on the consumer keeper because the evidence module keeper
// in cosmos-sdk v0.50 requires this exact method with this exact signature to be available on the StakingKeepr
func (k Keeper) GetParams(context.Context) (stakingtypes.Params, error) {
	return stakingtypes.Params{}, nil
}

// GetEnabled returns the enabled flag for the consumer module
func (k Keeper) GetEnabled(ctx sdk.Context) bool {
	params := k.GetConsumerParams(ctx)
	return params.Enabled
}

// GetCCVTimeoutPeriod returns the timeout period for sent ccv related ibc packets
func (k Keeper) GetCCVTimeoutPeriod(ctx sdk.Context) time.Duration {
	params := k.GetConsumerParams(ctx)
	return params.CcvTimeoutPeriod
}

// GetHistoricalEntries returns the number of historical info entries to persist in store
func (k Keeper) GetHistoricalEntries(ctx sdk.Context) int64 {
	params := k.GetConsumerParams(ctx)
	return params.HistoricalEntries
}

// Only used to set an unbonding period in diff tests
func (k Keeper) SetUnbondingPeriod(ctx sdk.Context, period time.Duration) {
	params := k.GetConsumerParams(ctx)
	params.UnbondingPeriod = period
	k.SetParams(ctx, params)
}

func (k Keeper) GetUnbondingPeriod(ctx sdk.Context) time.Duration {
	params := k.GetConsumerParams(ctx)
	return params.UnbondingPeriod
}
