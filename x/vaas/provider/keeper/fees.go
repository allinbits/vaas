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

// DistributeConsumerFees collects fees from each launched consumer's fee pool
// and distributes them directly to bonded validators. The bonded validator set
// is queried once; each consumer pays fees_per_epoch (fees_per_block *
// blocks_per_epoch), split equally as share = fees_per_epoch / num_bonded.
// Validators flagged with downtime are skipped: their share simply stays in
// the consumer's fee pool (never leaves).
//
// If a consumer's pool has insufficient spendable balance, the consumer is
// marked as in debt and skipped. Non-insufficient-funds errors are logged and
// skipped without flipping the debt flag.
func (k Keeper) DistributeConsumerFees(ctx sdk.Context) error {
	defaultFeePerBlock := k.GetFeesPerBlock(ctx)
	blocksPerEpoch := k.GetBlocksPerEpoch(ctx)
	feePerEpoch := sdk.NewCoin(defaultFeePerBlock.Denom, defaultFeePerBlock.Amount.MulRaw(blocksPerEpoch))

	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}
	if len(bonded) == 0 {
		return nil
	}
	numBonded := math.NewInt(int64(len(bonded)))

	// Precompute eligible (non-downtime) validator addresses.
	type eligibleVal struct {
		operator string
		addr     sdk.AccAddress
	}
	var eligible []eligibleVal
	for _, val := range bonded {
		consAddr, err := val.GetConsAddr()
		if err != nil {
			return fmt.Errorf("failed to get consensus address for validator %s: %w", val.GetOperator(), err)
		}
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
		eligible = append(eligible, eligibleVal{operator: val.GetOperator(), addr: sdk.AccAddress(valAddr)})
	}
	if len(eligible) == 0 {
		return nil
	}

	consumerIds := k.GetAllActiveConsumerIds(ctx)
	for _, consumerId := range consumerIds {
		if k.GetConsumerPhase(ctx, consumerId) != types.CONSUMER_PHASE_LAUNCHED {
			continue
		}

		feesPerEpoch, _ := k.effectiveFeesPerEpoch(ctx, consumerId, feePerEpoch, defaultFeePerBlock)
		share := feesPerEpoch.Amount.Quo(numBonded)
		if share.IsZero() {
			continue
		}
		shareCoins := sdk.NewCoins(sdk.NewCoin(feesPerEpoch.Denom, share))
		consumerFeePoolAddr := k.GetConsumerFeePoolAddress(consumerId)

		sendErr := false
		for _, ev := range eligible {
			if err := k.bankKeeper.SendCoins(ctx, consumerFeePoolAddr, ev.addr, shareCoins); err != nil {
				if errorsmod.IsOf(err, sdkerrors.ErrInsufficientFunds) {
					k.UpdateConsumerDebtStatus(ctx, consumerId, true)
					k.Logger(ctx).Debug("consumer fee pool underfunded; skipping remaining validators",
						"consumerId", consumerId,
						"failedValidator", ev.operator,
					)
					sendErr = true
					break
				}
				k.Logger(ctx).Error("failed to distribute fees to validator; chain-config issue likely",
					"consumerId", consumerId,
					"validator", ev.operator,
					"amount", shareCoins.String(),
					"err", err,
				)
				sendErr = true
				break
			}
		}
		if !sendErr {
			k.UpdateConsumerDebtStatus(ctx, consumerId, false)
			k.Logger(ctx).Debug("distributed consumer fees to validators",
				"consumerId", consumerId,
				"sharePerValidator", shareCoins.String(),
			)
		}
	}

	return nil
}
