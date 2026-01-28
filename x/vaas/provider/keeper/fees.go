package keeper

import (
	"fmt"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// CollectFeesFromConsumers collects fees from all active consumer chains.
// For each consumer, it checks if the consumer has enough balance to pay the fees.
// If a consumer doesn't have enough funds, the provider should inform the consumer
// to stop operations.
func (k Keeper) CollectFeesFromConsumers(ctx sdk.Context, feesPerBlock sdk.Coin) (sdk.Coin, error) {
	totalFeesCollected := sdk.NewCoin(feesPerBlock.Denom, math.ZeroInt())

	// Get all active consumer IDs
	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		// Get the consumer's module account address
		// Each consumer has a module account in the format "consumer-<consumerId>"
		consumerModuleAccAddr := authtypes.NewModuleAddress(fmt.Sprintf("consumer-%s", consumerId))

		// Check the balance of the consumer account
		balance := k.bankKeeper.GetBalance(ctx, consumerModuleAccAddr, feesPerBlock.Denom)
		if balance.IsLT(feesPerBlock) {
			// Consumer doesn't have enough funds
			k.Logger(ctx).Error("consumer chain has insufficient funds",
				"consumerId", consumerId,
				"balance", balance.Amount.String(),
				"required", feesPerBlock.String(),
			)

			// TODO: Implement mechanism to inform the consumer chain to stop
			// This could involve setting the phase to CONSUMER_PHASE_STOPPED
			// or sending a message to the consumer chain
			continue
		}

		// Transfer fees from consumer account to fee collector
		consumerModuleName := fmt.Sprintf("consumer-%s", consumerId)
		feeCoins := sdk.NewCoins(feesPerBlock)
		if err := k.bankKeeper.SendCoinsFromModuleToModule(ctx, consumerModuleName, k.feeCollectorName, feeCoins); err != nil {
			return totalFeesCollected, fmt.Errorf("failed to collect fees from consumer (%s): %w", consumerId, err)
		}

		k.Logger(ctx).Debug("collected fees from consumer",
			"consumerId", consumerId,
			"amount", feeCoins.String(),
		)

		totalFeesCollected = totalFeesCollected.Add(feesPerBlock)
	}
	return totalFeesCollected, nil
}

// DistributeFeesToValidators distributes the collected fees to validators
// proportionally based on their voting power.
func (k Keeper) DistributeFeesToValidators(ctx sdk.Context, totalFees sdk.Coin) error {
	// Get the bonded validators
	validators, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}

	if len(validators) == 0 {
		// No validators to distribute to, return early
		return nil
	}

	// Calculate total voting power
	totalPower := math.ZeroInt()
	for _, val := range validators {
		totalPower = totalPower.Add(math.NewInt(val.GetConsensusPower(sdk.DefaultPowerReduction)))
	}

	if totalPower.IsZero() {
		return fmt.Errorf("total voting power is zero")
	}

	// Distribute fees proportionally to each validator based on voting power
	for _, val := range validators {
		// Get validator operator address
		valAddr, err := k.stakingKeeper.ValidatorAddressCodec().StringToBytes(val.GetOperator())
		if err != nil {
			return fmt.Errorf("failed to parse validator address: %w", err)
		}

		// Calculate validator's share: (validatorPower / totalPower) * totalFees
		valPower := math.NewInt(val.GetConsensusPower(sdk.DefaultPowerReduction))
		valShare := totalFees.Amount.Mul(valPower).Quo(totalPower)
		if valShare.IsZero() {
			continue
		}

		// Send coins from fee_collector to validator account
		coins := sdk.NewCoins(sdk.NewCoin(totalFees.Denom, valShare))
		if err := k.bankKeeper.SendCoinsFromModuleToAccount(ctx, "fee_collector", valAddr, coins); err != nil {
			return fmt.Errorf("failed to send fees to validator %s: %w", val.GetOperator(), err)
		}
	}
	
	return nil
}
