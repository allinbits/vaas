package keeper

import (
	"context"
	"time"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// GetParams returns the params for the consumer VAAS module
// NOTE: it is different from the GetParams method which is required to implement StakingKeeper interface
func (k Keeper) GetConsumerParams(ctx sdk.Context) vaastypes.ConsumerParams {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ParametersKey())
	var params vaastypes.ConsumerParams
	k.cdc.MustUnmarshal(bz, &params)
	return params
}

// SetParams sets the paramset for the consumer module
func (k Keeper) SetParams(ctx sdk.Context, params vaastypes.ConsumerParams) {
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

// GetVAASTimeoutPeriod returns the timeout period for sent VAAS related ibc packets
func (k Keeper) GetVAASTimeoutPeriod(ctx sdk.Context) time.Duration {
	params := k.GetConsumerParams(ctx)
	return params.VaasTimeoutPeriod
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
