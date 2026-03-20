package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// GetConsumerFeePoolAddress returns the deterministic provider-side fee pool
// account for a consumer. This is a plain account address used for fee funding,
// not a registered module account in the app's module-account permissions.
func (k Keeper) GetConsumerFeePoolAddress(consumerId string) sdk.AccAddress {
	return authtypes.NewModuleAddress(fmt.Sprintf("%s-consumer-fee-pool-%s", types.ModuleName, consumerId))
}

// CollectFeesFromConsumers collects fees from all active consumer chains.
// For each consumer, it checks if the consumer has enough balance to pay the fees.
// If a consumer doesn't have enough funds, the provider should inform the consumer
// to stop operations. Unexpected per-consumer transfer failures are logged and skipped
// so one bad consumer account does not block collection from the rest.
func (k Keeper) CollectFeesFromConsumers(ctx sdk.Context) (sdk.Coin, error) {
	feesPerBlock := k.GetFeesPerBlock(ctx)
	totalFeesCollected := sdk.NewCoin(feesPerBlock.Denom, math.ZeroInt())

	// Get all active consumer IDs
	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		// Only collect fees from LAUNCHED consumers
		// REGISTERED and INITIALIZED consumers are not yet running and shouldn't be charged
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		// Check the balance of the consumer fee pool account.
		balance := k.bankKeeper.GetBalance(ctx, consumerFeePoolAddr, feesPerBlock.Denom)
		wasInDebt := k.IsConsumerInDebt(ctx, consumerId)
		if balance.IsLT(feesPerBlock) {
			k.UpdateConsumerDebtStatus(ctx, consumerId, true)
			if wasInDebt {
				k.Logger(ctx).Debug("consumer chain remains in debt",
					"consumerId", consumerId,
					"balance", balance.Amount.String(),
					"required", feesPerBlock.String(),
				)
			}
			continue
		}

		// Transfer fees from the consumer fee pool into the provider module account.
		feeCoins := sdk.NewCoins(feesPerBlock)
		if err := k.bankKeeper.SendCoinsFromAccountToModule(ctx, consumerFeePoolAddr, types.ModuleName, feeCoins); err != nil {
			k.Logger(ctx).Error("failed to collect fees from consumer",
				"consumerId", consumerId,
				"amount", feeCoins.String(),
				"err", err,
			)
			k.UpdateConsumerDebtStatus(ctx, consumerId, true)
			continue
		}
		k.UpdateConsumerDebtStatus(ctx, consumerId, false)
		k.Logger(ctx).Debug("collected fees from consumer",
			"consumerId", consumerId,
			"amount", feeCoins.String(),
		)

		totalFeesCollected = totalFeesCollected.Add(feesPerBlock)
	}
	return totalFeesCollected, nil
}

// DistributeFeesToValidators distributes the provider module account's
// currently available consumer-fee balance to validators proportionally
// based on their voting power.
func (k Keeper) DistributeFeesToValidators(ctx sdk.Context) error {
	totalFees := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), k.GetFeesPerBlock(ctx).Denom)
	if totalFees.IsZero() {
		return nil
	}

	// Get the bonded validators
	validators, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}

	if len(validators) == 0 {
		// No validators to distribute to, return early
		return nil
	}

	// Retrieve total voting power from staking keeper state.
	totalPower, err := k.stakingKeeper.GetLastTotalPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get total voting power: %w", err)
	}

	if totalPower.IsZero() {
		return fmt.Errorf("total voting power is zero")
	}

	// Track distributed amount to handle remainder
	distributedAmount := math.ZeroInt()
	hasProportionalShare := false
	powerReduction := k.stakingKeeper.PowerReduction(ctx)

	// Distribute fees proportionally to each validator based on voting power
	for i, val := range validators {
		// Calculate validator's share: (validatorPower / totalPower) * totalFees
		valPower := math.NewInt(val.GetConsensusPower(powerReduction))
		valShare := totalFees.Amount.Mul(valPower).Quo(totalPower)
		if valShare.IsPositive() {
			hasProportionalShare = true
		}

		// If this is the last validator, assign the remainder to ensure all fees are distributed
		if i == len(validators)-1 {
			// Skip distribution when all proportional shares round down to zero.
			// In this case, keeping funds in the provider module account avoids sending all fees
			// to the last validator purely due to integer division remainder.
			if !hasProportionalShare {
				return nil
			}
			valShare = totalFees.Amount.Sub(distributedAmount)
		}

		if valShare.IsZero() {
			continue
		}

		// Get validator operator address
		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return fmt.Errorf("failed to parse validator address: %w", err)
		}

		// Transfer fees from the provider module account to the validator account.
		feeCoins := sdk.NewCoins(sdk.NewCoin(totalFees.Denom, valShare))
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, types.ModuleName, sdk.AccAddress(valAddr), feeCoins); err != nil {
			return fmt.Errorf("failed to distribute fees to validator (%s): %w", val.GetOperator(), err)
		}

		distributedAmount = distributedAmount.Add(valShare)
	}

	return nil
}
