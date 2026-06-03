package keeper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	commitmenttypes "github.com/cosmos/ibc-go/v10/modules/core/23-commitment/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// GetTemplateClient returns a template Tendermint client state with default values.
// The returned client state is a starting point that gets customized per-consumer
// (chain ID, heights, trusting/unbonding periods) before client creation.
func (k Keeper) GetTemplateClient(ctx context.Context) *ibctmtypes.ClientState {
	return ibctmtypes.NewClientState(
		"",
		ibctmtypes.DefaultTrustLevel,
		0,
		0,
		types.DefaultMaxClockDrift,
		clienttypes.Height{},
		commitmenttypes.GetSDKSpecs(),
		[]string{"upgrade", "upgradedIBCState"},
	)
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

// GetEffectiveFeesPerBlock returns the per-block fee charged to a specific
// consumer: the override amount if one is set, else Params.FeesPerBlock.
// The returned Coin always carries Params.FeesPerBlock.Denom. The bool
// reports whether an override was applied (true) or the default was used
// (false).
func (k Keeper) GetEffectiveFeesPerBlock(ctx context.Context, consumerId uint64) (sdk.Coin, bool) {
	return k.effectiveFeesPerBlock(ctx, consumerId, k.GetFeesPerBlock(ctx))
}

// effectiveFeesPerBlock resolves the per-consumer fee given an already-read
// default. The per-block fee collection loop reads the default once and reuses
// it across all consumers via this method, avoiding a redundant params read
// per consumer.
func (k Keeper) effectiveFeesPerBlock(ctx context.Context, consumerId uint64, defaultCoin sdk.Coin) (sdk.Coin, bool) {
	overrideAmt, err := k.ConsumerFeesPerBlockOverride.Get(ctx, consumerId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return defaultCoin, false
		}
		panic(fmt.Errorf("error getting fees-per-block override for consumer %d: %w", consumerId, err))
	}
	return sdk.NewCoin(defaultCoin.Denom, overrideAmt), true
}

// reconcileFeesPerBlockOverrides drops every per-consumer override that is no
// longer strictly greater than floor (the module-wide Params.FeesPerBlock
// amount). It is called when the global fees_per_block rises so the "override
// must exceed the default" floor holds as a true invariant, not just at set
// time: a higher default can leave overrides underwater, and those consumers
// should revert to paying the new (higher) default.
//
// UpdateParams is a rare governance action and only a handful of overrides are
// expected, so the full walk here is expected to be manageable.
func (k Keeper) reconcileFeesPerBlockOverrides(ctx context.Context, floor math.Int) error {
	var toRemove []uint64
	err := k.ConsumerFeesPerBlockOverride.Walk(ctx, nil, func(consumerId uint64, amt math.Int) (bool, error) {
		if !amt.GT(floor) {
			toRemove = append(toRemove, consumerId)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	for _, consumerId := range toRemove {
		if err := k.ConsumerFeesPerBlockOverride.Remove(ctx, consumerId); err != nil {
			return err
		}
	}
	return nil
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

// GetInfractionParams returns the infraction parameters for consumer chain slashing.
func (k Keeper) GetInfractionParams(ctx context.Context) types.InfractionParameters {
	params, err := k.InfractionParams.Get(ctx)
	if err != nil {
		panic(fmt.Sprintf("error getting infraction parameters: %v", err))
	}
	return params
}

// SetInfractionParams sets the infraction parameters for consumer chain slashing.
func (k Keeper) SetInfractionParams(ctx context.Context, params types.InfractionParameters) {
	if err := k.InfractionParams.Set(ctx, params); err != nil {
		panic(fmt.Sprintf("error setting infraction parameters: %v", err))
	}
}
