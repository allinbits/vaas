package keeper

import (
	"context"
	"errors"
	"fmt"

	providertypes "github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	abci "github.com/cometbft/cometbft/abci/types"

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

func (k Keeper) HasPendingConsumerDebtPacket(ctx context.Context, consumerId string) bool {
	pending, err := k.PendingConsumerDebtPackets.Get(ctx, consumerId)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return false
		}
		panic(fmt.Errorf("failed to read pending consumer debt packet status for %s: %w", consumerId, err))
	}
	return pending
}

func (k Keeper) SetPendingConsumerDebtPacket(ctx context.Context, consumerId string) {
	if err := k.PendingConsumerDebtPackets.Set(ctx, consumerId, true); err != nil {
		panic(fmt.Errorf("failed to mark pending consumer debt packet for %s: %w", consumerId, err))
	}
}

func (k Keeper) ClearPendingConsumerDebtPacket(ctx context.Context, consumerId string) {
	if err := k.PendingConsumerDebtPackets.Remove(ctx, consumerId); err != nil && !errors.Is(err, collections.ErrNotFound) {
		panic(fmt.Errorf("failed to clear pending consumer debt packet for %s: %w", consumerId, err))
	}
}

func (k Keeper) UpdateConsumerDebtStatus(ctx sdk.Context, consumerId string, inDebt bool) {
	wasInDebt := k.IsConsumerInDebt(ctx, consumerId)
	if wasInDebt == inDebt {
		return
	}

	k.SetConsumerInDebt(ctx, consumerId, inDebt)
	k.SetPendingConsumerDebtPacket(ctx, consumerId)

	if inDebt {
		k.Logger(ctx).Info("consumer chain entered debt status", "consumerId", consumerId)
		return
	}

	k.Logger(ctx).Info("consumer chain cleared debt status", "consumerId", consumerId)
}

func (k Keeper) QueuePendingConsumerDebtPackets(ctx sdk.Context, valUpdateID uint64) {
	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		if k.GetConsumerPhase(ctx, consumerId) != providertypes.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		if !k.HasPendingConsumerDebtPacket(ctx, consumerId) {
			continue
		}
		if _, found := k.GetConsumerIdToChannelId(ctx, consumerId); !found {
			continue
		}

		packet := vaastypes.NewValidatorSetChangePacketData([]abci.ValidatorUpdate{}, valUpdateID)
		packet.ConsumerInDebt = k.IsConsumerInDebt(ctx, consumerId)
		k.AppendPendingVSCPackets(ctx, consumerId, packet)
		k.ClearPendingConsumerDebtPacket(ctx, consumerId)
	}
}

func (k Keeper) HasQueuedVSCPackets(ctx sdk.Context) bool {
	for _, consumerId := range k.GetAllConsumersWithIBCClients(ctx) {
		if k.GetConsumerPhase(ctx, consumerId) != providertypes.CONSUMER_PHASE_LAUNCHED {
			continue
		}
		if len(k.GetPendingVSCPackets(ctx, consumerId)) > 0 {
			return true
		}
	}
	return false
}
