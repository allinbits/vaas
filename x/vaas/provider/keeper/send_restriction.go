package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	disttypes "github.com/cosmos/cosmos-sdk/x/distribution/types"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// FeePoolSendRestriction returns a bank send-restriction that rejects sends
// to a known consumer fee pool address unless the sender is the provider
// module account or the distribution module account. The fee pool of a
// consumer in phase DELETED accepts nothing from anyone: the pool was settled
// by the auto-sweep on deletion and nothing ever sweeps it again, so any
// funds reaching it would be stranded for good.
func (k Keeper) FeePoolSendRestriction() func(
	ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
) (sdk.AccAddress, error) {
	providerAddr := authtypes.NewModuleAddress(types.ModuleName)
	distrAddr := authtypes.NewModuleAddress(disttypes.ModuleName)
	return func(
		ctx context.Context, fromAddr, toAddr sdk.AccAddress, amount sdk.Coins,
	) (sdk.AccAddress, error) {
		consumerId, err := k.FeePoolAddressToConsumerId.Get(ctx, toAddr)
		if err != nil {
			if errors.Is(err, collections.ErrNotFound) {
				return toAddr, nil
			}
			return nil, errorsmod.Wrapf(err,
				"fee-pool send restriction lookup for %s", toAddr.String())
		}
		if k.GetConsumerPhase(ctx, consumerId) == types.CONSUMER_PHASE_DELETED {
			return nil, errorsmod.Wrapf(types.ErrInvalidPhase,
				"fee pool %s belongs to deleted consumer %d and accepts no funds",
				toAddr.String(), consumerId)
		}
		if fromAddr.Equals(providerAddr) || fromAddr.Equals(distrAddr) {
			return toAddr, nil
		}
		return nil, errorsmod.Wrapf(types.ErrUnsolicitedFeePoolDeposit,
			"direct send to consumer fee pool %s blocked", toAddr.String())
	}
}
