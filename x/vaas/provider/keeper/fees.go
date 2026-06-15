package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

// GetConsumerFeePoolAddress returns the deterministic provider-side fee pool
// account for a consumer. This is a plain account address used for fee funding,
// not a registered module account in the app's module-account permissions.
func (k Keeper) GetConsumerFeePoolAddress(consumerId uint64) sdk.AccAddress {
	return authtypes.NewModuleAddress(fmt.Sprintf("%s-consumer-fee-pool-%d", types.ModuleName, consumerId))
}

// MarkEpochDowntime records a validator's provider consensus address as
// having downtime evidence in the current epoch for the given consumer.
func (k Keeper) MarkEpochDowntime(ctx sdk.Context, consumerId uint64, providerConsAddr sdk.ConsAddress) {
	if err := k.EpochDowntime.Set(ctx, collections.Join(consumerId, providerConsAddr.Bytes()), true); err != nil {
		panic(fmt.Errorf("failed to mark epoch downtime for consumer %d, %x: %w", consumerId, providerConsAddr, err))
	}
}

// IsEpochDowntime returns true if the validator had downtime evidence this
// epoch on the given consumer.
func (k Keeper) IsEpochDowntime(ctx sdk.Context, consumerId uint64, providerConsAddr sdk.ConsAddress) bool {
	found, err := k.EpochDowntime.Has(ctx, collections.Join(consumerId, providerConsAddr.Bytes()))
	if err != nil {
		panic(fmt.Errorf("failed to check epoch downtime for consumer %d, %x: %w", consumerId, providerConsAddr, err))
	}
	return found
}

// ClearEpochDowntime removes all downtime records for the current epoch.
func (k Keeper) ClearEpochDowntime(ctx sdk.Context) {
	if err := k.EpochDowntime.Clear(ctx, nil); err != nil {
		panic(fmt.Errorf("failed to clear epoch downtime: %w", err))
	}
}

// DistributeConsumerFees collects fees from each launched consumer's fee pool
// and distributes them directly to bonded validators in a single bank
// InputOutputCoins call per consumer. The bonded validator set is queried once;
// each consumer pays fees_per_epoch (fees_per_block * blocks_per_epoch), split
// equally as share = fees_per_epoch / num_bonded. Validators flagged with
// downtime are excluded from the outputs — only the eligible validators'
// shares (share * num_eligible) are drawn from the consumer pool, so the
// withheld shares remain available for the consumer. This ensures the
// consumer never pays for validation work that did not happen, and avoids
// incentivizing validators to DOS competitors (the other validators' shares
// do not increase when someone is excluded).
//
// If a consumer's pool balance is below fees_per_epoch, the consumer is marked
// as in debt and skipped entirely (no partial distribution).
func (k Keeper) DistributeConsumerFees(ctx sdk.Context) error {
	defaultFeePerBlock := k.GetFeesPerBlock(ctx)
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	defaultFeePerEpoch := sdk.NewCoin(defaultFeePerBlock.Denom, defaultFeePerBlock.Amount.MulRaw(blocksPerEpoch))

	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}
	if len(bonded) == 0 {
		return nil
	}
	numBonded := math.NewInt(int64(len(bonded)))

	// Precompute consensus and account addresses for all bonded validators.
	type bondedVal struct {
		consAddr sdk.ConsAddress
		accAddr  sdk.AccAddress
	}
	bondedVals := make([]bondedVal, len(bonded))
	for i, val := range bonded {
		consAddr, err := val.GetConsAddr()
		if err != nil {
			return fmt.Errorf("failed to get consensus address for validator %s: %w", val.GetOperator(), err)
		}
		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return fmt.Errorf("failed to parse validator address %s: %w", val.GetOperator(), err)
		}
		bondedVals[i] = bondedVal{consAddr: consAddr, accAddr: valAddr}
	}

	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}

		// Filter eligible validators for this consumer.
		var eligibleAddrs []sdk.AccAddress
		for _, bv := range bondedVals {
			if k.IsEpochDowntime(ctx, consumerId, bv.consAddr) {
				continue
			}
			eligibleAddrs = append(eligibleAddrs, bv.accAddr)
		}
		if len(eligibleAddrs) == 0 {
			continue
		}

		consumerFeePerEpoch, _ := k.effectiveFeesPerEpoch(ctx, consumerId, defaultFeePerEpoch, defaultFeePerBlock)
		share := consumerFeePerEpoch.Amount.Quo(numBonded)
		if share.IsZero() {
			continue
		}
		shareCoin := sdk.NewCoin(consumerFeePerEpoch.Denom, share)
		shareCoins := sdk.NewCoins(shareCoin)
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		// Check the pool can cover the full epoch fee before paying any
		// validator. This avoids unfair partial distribution.
		balance := k.bankKeeper.GetBalance(ctx, consumerFeePoolAddr, consumerFeePerEpoch.Denom)
		if balance.Amount.LT(consumerFeePerEpoch.Amount) {
			k.UpdateConsumerDebtStatus(ctx, consumerId, true)
			k.Logger(ctx).Debug("consumer fee pool underfunded; skipping distribution",
				"consumerId", consumerId,
				"balance", balance.String(),
				"required", consumerFeePerEpoch.String(),
			)
			continue
		}

		// Single InputOutputCoins: consumer pool → all eligible validators.
		// Only share*num_eligible is drawn; excluded validators' shares stay
		// in the consumer pool.
		totalOut := share.MulRaw(int64(len(eligibleAddrs)))
		outputs := make([]banktypes.Output, len(eligibleAddrs))
		for i, addr := range eligibleAddrs {
			outputs[i] = banktypes.Output{Address: addr.String(), Coins: shareCoins}
		}
		input := banktypes.Input{
			Address: consumerFeePoolAddr.String(),
			Coins:   sdk.NewCoins(sdk.NewCoin(consumerFeePerEpoch.Denom, totalOut)),
		}
		// Run in a cached context so a mid-distribution error (e.g. a send
		// restriction on one output) rolls back the entire operation instead
		// of leaving some validators paid and others not.
		cachedCtx, write := ctx.CacheContext()
		if err := k.bankKeeper.InputOutputCoins(cachedCtx, input, outputs); err != nil {
			k.Logger(ctx).Error("failed to distribute consumer fees",
				"consumerId", consumerId,
				"err", err,
			)
			continue
		}
		write()
		k.UpdateConsumerDebtStatus(ctx, consumerId, false)
		k.Logger(ctx).Debug("distributed consumer fees to validators",
			"consumerId", consumerId,
			"sharePerValidator", shareCoins.String(),
			"totalDistributed", sdk.NewCoins(sdk.NewCoin(consumerFeePerEpoch.Denom, totalOut)).String(),
		)
	}

	return nil
}
