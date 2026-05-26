package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
)

func (k Keeper) IsConsumerInDebt(ctx context.Context) bool {
	inDebt, err := k.ConsumerInDebt.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false
		}
		panic(fmt.Errorf("failed to read consumer debt status: %w", err))
	}
	return inDebt
}

func (k Keeper) SetConsumerInDebt(ctx context.Context, inDebt bool) {
	if err := k.ConsumerInDebt.Set(ctx, inDebt); err != nil {
		panic(fmt.Errorf("failed to set consumer debt status: %w", err))
	}
}
