package keeper

import (
	"fmt"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"

	"cosmossdk.io/math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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

// DistributeFeesToValidators splits the provider module account's currently
// available consumer-fee balance equally among the validators that both
// signed the previous block AND are still bonded in the current block.
//
// The service consumers pay for (being validated) is delivered roughly evenly
// by each signing validator, independent of their stake, so the pay matches
// the work rather than the stake. Bonded validators that did not sign are
// skipped, which penalizes downtime in the same way Cosmos SDK x/distribution
// does via BeginBlock VoteInfos. Signers that have since unbonded or been
// removed from the set forfeit their share — we only pay current participants.
func (k Keeper) DistributeFeesToValidators(ctx sdk.Context) error {
	totalFees := k.bankKeeper.GetBalance(ctx, authtypes.NewModuleAddress(types.ModuleName), k.GetFeesPerBlock(ctx).Denom)
	if totalFees.IsZero() {
		return nil
	}

	// Fetch the full bonded set once and index by consensus address. The
	// signers in VoteInfos are matched against this map so we only pay
	// validators that are still bonded in the current block.
	bonded, err := k.stakingKeeper.GetBondedValidatorsByPower(ctx)
	if err != nil {
		return fmt.Errorf("failed to get bonded validators: %w", err)
	}
	byCons := make(map[string]stakingtypes.Validator, len(bonded))
	for _, v := range bonded {
		consAddr, err := v.GetConsAddr()
		if err != nil {
			k.Logger(ctx).Debug("skipping bonded validator with unreadable consensus pubkey",
				"operator", v.GetOperator(),
				"err", err,
			)
			continue
		}
		byCons[string(consAddr)] = v
	}

	// VoteInfos carries the previous block's LastCommit with a BlockIdFlag
	// indicating whether each validator signed, voted nil, or was absent.
	eligible := make([]stakingtypes.Validator, 0, len(ctx.VoteInfos()))
	for _, vote := range ctx.VoteInfos() {
		if vote.BlockIdFlag != cmtproto.BlockIDFlagCommit {
			continue
		}
		v, ok := byCons[string(vote.Validator.Address)]
		if !ok {
			continue // signer no longer bonded
		}
		eligible = append(eligible, v)
	}
	if len(eligible) == 0 {
		return nil
	}

	// Equal split. Any integer-division remainder stays in the provider
	// module account and is picked up by the next block's GetBalance.
	share := totalFees.Amount.Quo(math.NewInt(int64(len(eligible))))
	if share.IsZero() {
		return nil
	}
	shareCoins := sdk.NewCoins(sdk.NewCoin(totalFees.Denom, share))

	for _, val := range eligible {
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
