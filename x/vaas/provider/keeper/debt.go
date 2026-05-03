package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) IsConsumerInDebt(ctx context.Context, consumerId string) bool {
	inDebt, err := k.ConsumerDebt.Get(ctx, consumerId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false
		}
		panic(fmt.Errorf("failed to read consumer debt status for %s: %w", consumerId, err))
	}
	return inDebt
}

func (k Keeper) SetConsumerInDebt(ctx context.Context, consumerId string, inDebt bool) {
	if err := k.ConsumerDebt.Set(ctx, consumerId, inDebt); err != nil {
		panic(fmt.Errorf("failed to set consumer debt status for %s: %w", consumerId, err))
	}
}

func (k Keeper) DeleteConsumerDebt(ctx context.Context, consumerId string) {
	if err := k.ConsumerDebt.Remove(ctx, consumerId); err != nil && !errors.Is(err, collections.ErrNotFound) {
		panic(fmt.Errorf("failed to delete consumer debt status for %s: %w", consumerId, err))
	}
}

// UpdateConsumerDebtStatus persists a new debt state and logs state transitions.
// The updated flag reaches the consumer on the next epoch VSC packet, which
// piggybacks the current debt state on the normal validator-set change flow.
func (k Keeper) UpdateConsumerDebtStatus(ctx sdk.Context, consumerId string, inDebt bool) {
	if k.IsConsumerInDebt(ctx, consumerId) == inDebt {
		return
	}

	k.SetConsumerInDebt(ctx, consumerId, inDebt)

	if inDebt {
		k.Logger(ctx).Info("consumer chain entered debt status", "consumerId", consumerId)
		return
	}

	k.Logger(ctx).Info("consumer chain cleared debt status", "consumerId", consumerId)
}
