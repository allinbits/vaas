package keeper

import (
	"context"
	"time"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// GetConsumerParams returns the params for the consumer VAAS module
// NOTE: it is different from the GetParams method which is required to implement StakingKeeper interface
func (k Keeper) GetConsumerParams(ctx context.Context) vaastypes.ConsumerParams {
	params, err := k.Params.Get(ctx)
	if err != nil {
		return vaastypes.ConsumerParams{}
	}
	return params
}

// SetParams sets the paramset for the consumer module
func (k Keeper) SetParams(ctx context.Context, params vaastypes.ConsumerParams) {
	if err := k.Params.Set(ctx, params); err != nil {
		panic(err)
	}
}

// GetParams implements StakingKeeper GetParams interface method
// it returns an a empty stakingtypes.Params struct
// NOTE: this method must be implemented on the consumer keeper because the evidence module keeper
// in cosmos-sdk v0.50 requires this exact method with this exact signature to be available on the StakingKeepr
func (k Keeper) GetParams(context.Context) (stakingtypes.Params, error) {
	return stakingtypes.Params{}, nil
}

// GetEnabled returns the enabled flag for the consumer module
func (k Keeper) GetEnabled(ctx context.Context) bool {
	params := k.GetConsumerParams(ctx)
	return params.Enabled
}

// GetVAASTimeoutPeriod returns the timeout period for sent VAAS related ibc packets
func (k Keeper) GetVAASTimeoutPeriod(ctx context.Context) time.Duration {
	params := k.GetConsumerParams(ctx)
	return params.VaasTimeoutPeriod
}

// GetHistoricalEntries returns the number of historical info entries to persist in store
func (k Keeper) GetHistoricalEntries(ctx context.Context) int64 {
	params := k.GetConsumerParams(ctx)
	return params.HistoricalEntries
}

// Only used to set an unbonding period in diff tests
func (k Keeper) SetUnbondingPeriod(ctx context.Context, period time.Duration) {
	params := k.GetConsumerParams(ctx)
	params.UnbondingPeriod = period
	k.SetParams(ctx, params)
}

func (k Keeper) GetUnbondingPeriod(ctx context.Context) time.Duration {
	params := k.GetConsumerParams(ctx)
	return params.UnbondingPeriod
}
