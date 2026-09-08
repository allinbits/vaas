package keeper

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

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

// GetMaxPauseDuration returns the maximum time a consumer chain may remain in
// the PAUSED phase before the provider automatically stops it.
func (k Keeper) GetMaxPauseDuration(ctx context.Context) time.Duration {
	params := k.GetParams(ctx)
	return params.MaxPauseDuration
}

// GetFeesPerBlock returns the fees that each consumer chain must pay per block.
// The amount is governed via Params.FeesPerBlockAmount while the denom is a
// keeper-wired constant and cannot be changed without a binary upgrade.
func (k Keeper) GetFeesPerBlock(ctx context.Context) sdk.Coin {
	return sdk.NewCoin(k.feeDenom, k.GetParams(ctx).FeesPerBlockAmount)
}

// GetEffectiveFeesPerBlock returns the per-block fee charged to a specific
// consumer: the override amount if one is set, else the default
// Params.FeesPerBlockAmount. The returned Coin always carries the module fee
// denom (Keeper.feeDenom). The bool reports whether an override was applied
// (true) or the default was used (false).
func (k Keeper) GetEffectiveFeesPerBlock(ctx context.Context, consumerId uint64) (sdk.Coin, bool) {
	return k.effectiveFeesPerBlock(ctx, consumerId, k.GetFeesPerBlock(ctx))
}

// effectiveFeesPerEpoch resolves the per-consumer epoch fee given an already-read
// default epoch fee and default per-block fee. If a per-consumer override exists,
// it's multiplied by blocks_per_epoch; otherwise the default epoch fee is used.
func (k Keeper) effectiveFeesPerEpoch(ctx context.Context, consumerId uint64, defaultEpochFee, defaultFeePerBlock sdk.Coin) (sdk.Coin, bool) {
	overrideAmt, err := k.ConsumerFeesPerBlockOverride.Get(ctx, consumerId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return defaultEpochFee, false
		}
		panic(fmt.Errorf("error getting fees-per-block override for consumer %d: %w", consumerId, err))
	}
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	return sdk.NewCoin(defaultFeePerBlock.Denom, overrideAmt.MulRaw(blocksPerEpoch)), true
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
// longer strictly greater than floor (the module-wide Params.FeesPerBlockAmount).
// It is called when the global fees_per_block rises so the "override
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
// If the downtime-window fields (SignedBlocksWindow, MinSignedPerWindow) differ
// from the currently stored params, the old values are recorded in
// PreviousDowntimeParams so downtime evidence computed under them remains
// acceptable for a bounded grace window; see AcceptableDowntimeParams.
func (k Keeper) SetInfractionParams(ctx context.Context, params types.InfractionParameters) {
	if has, err := k.InfractionParams.Has(ctx); err == nil && has {
		old := k.GetInfractionParams(ctx)
		if old.SignedBlocksWindow != params.SignedBlocksWindow || !old.MinSignedPerWindow.Equal(params.MinSignedPerWindow) {
			previous := types.PreviousDowntimeParams{
				Params: vaastypes.DowntimeParams{
					SignedBlocksWindow: old.SignedBlocksWindow,
					MinSignedPerWindow: old.MinSignedPerWindow,
				},
				ChangedAt: sdk.UnwrapSDKContext(ctx).BlockTime(),
			}
			if err := k.PreviousDowntimeParams.Set(ctx, previous); err != nil {
				panic(fmt.Sprintf("error setting previous downtime params: %v", err))
			}
		}
	}
	if err := k.InfractionParams.Set(ctx, params); err != nil {
		panic(fmt.Sprintf("error setting infraction parameters: %v", err))
	}
}

// CurrentDowntimeParams returns the provider's current downtime detection
// parameters, derived from InfractionParams, for distribution to consumers
// via genesis and VSC packets.
func (k Keeper) CurrentDowntimeParams(ctx sdk.Context) vaastypes.DowntimeParams {
	ip := k.GetInfractionParams(ctx)
	return vaastypes.DowntimeParams{
		SignedBlocksWindow: ip.SignedBlocksWindow,
		MinSignedPerWindow: ip.MinSignedPerWindow,
	}
}

// AcceptableDowntimeParams reports whether echoed downtime params are
// acceptable for verifying downtime evidence: either they match the
// provider's current params, or they match the immediately-prior params
// (PreviousDowntimeParams) and the change that superseded them happened no
// longer ago than DowntimeEvidenceMaxAge + DowntimeChallengeWindow.
func (k Keeper) AcceptableDowntimeParams(ctx sdk.Context, echoed vaastypes.DowntimeParams) bool {
	current := k.CurrentDowntimeParams(ctx)
	if echoed.SignedBlocksWindow == current.SignedBlocksWindow && echoed.MinSignedPerWindow.Equal(current.MinSignedPerWindow) {
		return true
	}

	previous, err := k.PreviousDowntimeParams.Get(ctx)
	if err != nil {
		return false
	}
	if echoed.SignedBlocksWindow != previous.Params.SignedBlocksWindow || !echoed.MinSignedPerWindow.Equal(previous.Params.MinSignedPerWindow) {
		return false
	}

	ip := k.GetInfractionParams(ctx)
	horizon := ip.DowntimeEvidenceMaxAge + ip.DowntimeChallengeWindow
	return ctx.BlockTime().Sub(previous.ChangedAt) <= horizon
}

// LivenessGracePeriod returns the maximum time a launched consumer may go
// without a successful VSC ack before it is removed, derived from the provider
// unbonding period and the LivenessGraceFraction param so it stays inside the
// slashable window.
func (k Keeper) LivenessGracePeriod(ctx context.Context) (time.Duration, error) {
	unbonding, err := k.stakingKeeper.UnbondingTime(ctx)
	if err != nil {
		return 0, err
	}
	return vaastypes.CalculateTrustPeriod(unbonding, k.GetParams(ctx).LivenessGraceFraction)
}
