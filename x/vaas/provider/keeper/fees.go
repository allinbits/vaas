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

// CollectFeesFromConsumers collects fees from all active consumer chains.
// For each consumer, it checks if the consumer has enough balance to pay the fees.
// If a consumer doesn't have enough funds, it is marked as in debt; the flag
// is then propagated on the next VSC packet so the consumer's ante gate
// blocks non-IBC, non-gov user transactions until the pool is funded again.
// Unexpected per-consumer transfer failures are logged and skipped so one
// bad consumer account does not block collection from the rest.
func (k Keeper) CollectFeesFromConsumers(ctx sdk.Context) sdk.Coin {
	defaultFee := k.GetFeesPerBlock(ctx)
	totalFeesCollected := sdk.NewCoin(defaultFee.Denom, math.ZeroInt())

	// Get all active consumer IDs
	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		// Only collect fees from LAUNCHED consumers
		// REGISTERED and INITIALIZED consumers are not yet running and shouldn't be charged
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		feesPerBlock, _ := k.effectiveFeesPerBlock(ctx, consumerId, defaultFee)
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		// Transfer fees from the consumer fee pool into the provider module account.
		// SendCoins enforces spendable balance, so the underfunded case (including
		// vesting lockup) surfaces here as ErrInsufficientFunds.
		feeCoins := sdk.NewCoins(feesPerBlock)
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

		totalFeesCollected = totalFeesCollected.Add(feesPerBlock)
	}
	return totalFeesCollected
}

// DistributeFeesToValidators splits the provider module account's currently
// available consumer-fee balance equally among all bonded validators.
//
// Every bonded validator receives an equal share regardless of whether it
// signed the previous block. Offline or absent validators are not excluded.
func (k Keeper) DistributeFeesToValidators(ctx sdk.Context) error {
	totalFees := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), k.GetFeesPerBlock(ctx).Denom)
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

	// Equal split. Any integer-division remainder stays in the provider
	// module account and is picked up by the next block's GetBalance.
	share := totalFees.Amount.Quo(math.NewInt(int64(len(bonded))))
	if share.IsZero() {
		return nil
	}
	shareCoins := sdk.NewCoins(sdk.NewCoin(totalFees.Denom, share))

	for _, val := range bonded {
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
