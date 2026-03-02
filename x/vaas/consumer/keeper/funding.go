package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) GetFeeCollectorAccountAddress(ctx context.Context) (sdk.AccAddress, bool) {
	feeCollectorAccount := k.authKeeper.GetModuleAccount(ctx, k.feeCollectorName)
	if feeCollectorAccount == nil {
		return nil, false
	}

	return feeCollectorAccount.GetAddress(), true
}

func (k Keeper) HasFeeCollectorFundsForAmount(ctx context.Context, requiredAmount sdk.Coins) bool {
	if requiredAmount.IsZero() {
		return true
	}

	feeCollectorAddr, found := k.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return false
	}

	for _, requiredCoin := range requiredAmount {
		if requiredCoin.Denom == "" || !requiredCoin.Amount.IsPositive() {
			continue
		}
		balance := k.bankKeeper.GetBalance(ctx, feeCollectorAddr, requiredCoin.Denom)
		if balance.Amount.GTE(requiredCoin.Amount) {
			return true
		}
	}

	return false
}
