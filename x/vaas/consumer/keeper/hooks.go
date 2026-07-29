package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	vaastypes "github.com/allinbits/vaas/x/vaas/types"
)

var _ vaastypes.ConsumerHooks = Keeper{}

// Hooks wrapper struct for ConsumerKeeper
type Hooks struct {
	k Keeper
}

// Hooks Return the wrapper struct
func (k Keeper) Hooks() Hooks {
	return Hooks{k}
}

func (k Keeper) AfterValidatorBonded(ctx context.Context, consAddr sdk.ConsAddress, valAddr sdk.ValAddress) error {
	if k.hooks != nil {
		err := k.hooks.AfterValidatorBonded(ctx, consAddr, valAddr)
		return err
	}
	return nil
}
