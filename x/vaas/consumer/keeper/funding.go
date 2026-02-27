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

func (k Keeper) HasFeeCollectorFunds(ctx context.Context, denoms []string) bool {
	feeCollectorAddr, found := k.GetFeeCollectorAccountAddress(ctx)
	if !found {
		return false
	}

	for _, denom := range denoms {
		if denom == "" {
			continue
		}
		if k.bankKeeper.GetBalance(ctx, feeCollectorAddr, denom).Amount.IsPositive() {
			return true
		}
	}

	return false
}
