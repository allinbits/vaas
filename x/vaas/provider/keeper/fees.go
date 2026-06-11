package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// GetConsumerFeePoolAddress returns the deterministic provider-side fee pool
// account for a consumer. This is a plain account address used for fee funding,
// not a registered module account in the app's module-account permissions.
func (k Keeper) GetConsumerFeePoolAddress(consumerId uint64) sdk.AccAddress {
	return authtypes.NewModuleAddress(fmt.Sprintf("%s-consumer-fee-pool-%d", types.ModuleName, consumerId))
}

// MarkEpochDowntime records a validator's provider consensus address as having
// downtime evidence in the current epoch.
func (k Keeper) MarkEpochDowntime(ctx sdk.Context, providerConsAddr sdk.ConsAddress) {
	if err := k.EpochDowntime.Set(ctx, providerConsAddr.Bytes(), true); err != nil {
		panic(fmt.Errorf("failed to mark epoch downtime for %x: %w", providerConsAddr, err))
	}
}

// IsEpochDowntime returns true if the validator had downtime evidence this epoch.
func (k Keeper) IsEpochDowntime(ctx sdk.Context, providerConsAddr sdk.ConsAddress) bool {
	found, err := k.EpochDowntime.Has(ctx, providerConsAddr.Bytes())
	if err != nil {
		panic(fmt.Errorf("failed to check epoch downtime for %x: %w", providerConsAddr, err))
	}
	return found
}

// ClearEpochDowntime removes all downtime records for the current epoch.
func (k Keeper) ClearEpochDowntime(ctx sdk.Context) {
	if err := k.EpochDowntime.Clear(ctx, nil); err != nil {
		panic(fmt.Errorf("failed to clear epoch downtime: %w", err))
	}
}

// CollectFeesFromConsumers collects fees from all active consumer chains.
// For each consumer, it charges fees_per_epoch (fees_per_block * blocks_per_epoch).
// If a consumer doesn't have enough funds, it is marked as in debt; the flag
// is then propagated on the next VSC packet so the consumer's ante gate
// blocks non-IBC, non-gov user transactions until the pool is funded again.
// Unexpected per-consumer transfer failures are logged and skipped so one
// bad consumer account does not block collection from the rest.
func (k Keeper) CollectFeesFromConsumers(ctx sdk.Context) sdk.Coin {
	defaultFeePerBlock := k.GetFeesPerBlock(ctx)
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	feePerEpoch := sdk.NewCoin(defaultFeePerBlock.Denom, defaultFeePerBlock.Amount.MulRaw(blocksPerEpoch))
	totalFeesCollected := sdk.NewCoin(defaultFeePerBlock.Denom, math.ZeroInt())

	// Get all active consumer IDs
	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		// Only collect fees from LAUNCHED consumers
		// REGISTERED and INITIALIZED consumers are not yet running and shouldn't be charged
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		feesPerEpoch, _ := k.effectiveFeesPerEpoch(ctx, consumerId, feePerEpoch, defaultFeePerBlock)
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		// Transfer fees from the consumer fee pool into the provider module account.
		// SendCoins enforces spendable balance, so the underfunded case (including
		// vesting lockup) surfaces here as ErrInsufficientFunds.
		feeCoins := sdk.NewCoins(feesPerEpoch)
		if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, consumerFeePoolAddr, types.ModuleName, feeCoins); err != nil {
			if errorsmod.IsOf(err, sdkerrors.ErrInsufficientFunds) {
				k.UpdateConsumerDebtStatus(ctx, consumerId, true)
				continue
			}
			// Any other error is a chain-config / bug condition (blocked
			// address, denom send disabled, send restriction, etc.), not a
			// consumer fault. Log loudly but leave the debt flag untouched
			// to avoid misleading the operator, who has a funded pool.
			k.Logger(ctx).Error("failed to collect fees from consumer; chain-config issue likely",
				"consumerId", consumerId,
				"amount", feeCoins.String(),
				"err", err,
			)
			continue
		}
		k.UpdateConsumerDebtStatus(ctx, consumerId, false)
		k.Logger(ctx).Debug("collected fees from consumer",
			"consumerId", consumerId,
			"amount", feeCoins.String(),
		)

		totalFeesCollected = totalFeesCollected.Add(feesPerEpoch)
	}
	return totalFeesCollected
}

// DistributeFeesToValidators splits the provider module account's currently
// available consumer-fee balance equally among all bonded validators.
// Validators flagged with downtime evidence during the current epoch are
// excluded from receiving rewards. The reward amount per eligible validator
// stays fixed (total / num_bonded); the excluded validator's share is NOT
// redistributed to others — it remains in the provider module account and
// is picked up by the next epoch's distribution.
//
// This avoids creating DOS incentives: validators cannot profit by causing
// others to miss blocks, and the consumer does not pay for services not
// rendered (the leftover carries forward rather than being burned).
func (k Keeper) DistributeFeesToValidators(ctx sdk.Context) error {
	totalFees := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), k.GetFeeDenom())
	if totalFees.IsZero() {
		return nil
	}

	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}
	if len(bonded) == 0 {
		return nil
	}

	// Equal split based on total bonded count. Excluded validators do NOT cause
	// the share to increase for others.
	share := totalFees.Amount.Quo(math.NewInt(int64(len(bonded))))
	if share.IsZero() {
		return nil
	}
	shareCoins := sdk.NewCoins(sdk.NewCoin(totalFees.Denom, share))

	for _, val := range bonded {
		consAddr, err := val.GetConsAddr()
		if err != nil {
			return fmt.Errorf("failed to get consensus address for validator %s: %w", val.GetOperator(), err)
		}

		// Skip validators flagged for downtime this epoch
		if k.IsEpochDowntime(ctx, consAddr) {
			k.Logger(ctx).Debug("skipping epoch reward for downtime validator",
				"validator", val.GetOperator(),
			)
			continue
		}

		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return fmt.Errorf("failed to parse validator address: %w", err)
		}
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(valAddr), shareCoins); err != nil {
			return fmt.Errorf("failed to distribute fees to validator (%s): %w", val.GetOperator(), err)
		}
	}

	return nil
}
