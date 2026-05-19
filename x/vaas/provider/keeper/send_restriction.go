package keeper

import (
	"context"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
)

// FeePoolSendRestriction returns a bank send-restriction that rejects any
// send to a known active consumer fee pool address unless the source is the
// provider module account itself.
func (k Keeper) FeePoolSendRestriction() func(
	ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
) (sdk.AccAddress, error) {
	providerAddr := authtypes.NewModuleAddress(types.ModuleName)
	return func(
		ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
	) (sdk.AccAddress, error) {
		if _, err := k.FeePoolAddressToConsumerId.Get(ctx, toAddr); err != nil {
			return toAddr, nil
		}
		if fromAddr.Equals(providerAddr) {
			return toAddr, nil
		}
		return nil, errorsmod.Wrapf(types.ErrUnsolicitedFeePoolDeposit,
			"direct send to consumer fee pool %s blocked", toAddr.String())
	}
}
