package keeper

import (
	"context"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"
)

// FeePoolSendRestriction returns a bank send-restriction that rejects sends
// to a known active consumer fee pool address unless the sender is the
// provider module account or the distribution module account.
func (k Keeper) FeePoolSendRestriction() func(
	ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
) (sdk.AccAddress, error) {
	providerAddr := authtypes.NewModuleAddress(types.ModuleName)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	return func(
		ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
	) (sdk.AccAddress, error) {
		isFeePool, err := k.FeePoolAddressToConsumerId.Has(ctx, toAddr)
		if err != nil {
			return nil, errorsmod.Wrapf(err,
				"fee-pool send restriction lookup for %s", toAddr.String())
		}
		if !isFeePool {
			return toAddr, nil
		}
		if fromAddr.Equals(providerAddr) || fromAddr.Equals(distrAddr) {
			return toAddr, nil
		}
		return nil, errorsmod.Wrapf(types.ErrUnsolicitedFeePoolDeposit,
			"direct send to consumer fee pool %s blocked", toAddr.String())
	}
}
