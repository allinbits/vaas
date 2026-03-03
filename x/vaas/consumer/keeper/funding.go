package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) GetFeeCollectorAccountAddress(context.Context) (sdk.AccAddress, bool) {
	if len(k.feeCollectorAddress) == 0 {
		return nil, false
	}
	return k.feeCollectorAddress, true
}

func (k Keeper) HasFeeCollectorFundsForCoin(ctx context.Context, requiredCoin sdk.Coin) bool {
	if !requiredCoin.Amount.IsPositive() || requiredCoin.Denom == "" {
		return true
	}

	feeCollectorAddr, found := k.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return false
	}

	balance := k.bankKeeper.GetBalance(ctx, feeCollectorAddr, requiredCoin.Denom)
	return balance.Amount.GTE(requiredCoin.Amount)
}
