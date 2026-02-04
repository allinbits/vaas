package keeper

import (
	"context"
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/provider/types"
)

// GetConsumerId returns the next to-be-assigned consumer id
// Returns (0, false) if the sequence has never been used (no consumers created yet)
func (k Keeper) GetConsumerId(ctx context.Context) (uint64, bool) {
	consumerId, err := k.ConsumerId.Peek(ctx)
	if err != nil {
		return 0, false
	}
	// If consumerId is 0, it means no consumer has been created yet
	// (Next() hasn't been called, so the sequence is at its initial state)
	if consumerId == 0 {
		return 0, false
	}
	return consumerId, true
}

// FetchAndIncrementConsumerId fetches the first consumer id that can be used and increments the
// underlying consumer id
func (k Keeper) FetchAndIncrementConsumerId(ctx context.Context) string {
	consumerId, err := k.ConsumerId.Next(ctx)
	if err != nil {
		panic(fmt.Errorf("failed to get next consumer id: %w", err))
	}
	return strconv.FormatUint(consumerId, 10)
}

// GetConsumerChainId returns the chain id associated with this consumer id
func (k Keeper) GetConsumerChainId(ctx context.Context, consumerId string) (string, error) {
	chainId, err := k.ConsumerChainId.Get(ctx, consumerId)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve chain id for consumer id (%s): %w", consumerId, err)
	}
	return chainId, nil
}

// SetConsumerChainId sets the chain id associated with this consumer id
func (k Keeper) SetConsumerChainId(ctx context.Context, consumerId, chainId string) {
	if err := k.ConsumerChainId.Set(ctx, consumerId, chainId); err != nil {
		panic(fmt.Errorf("failed to set chain id for consumer id (%s): %w", consumerId, err))
	}
}

// DeleteConsumerChainId deletes the chain id associated with this consumer id
func (k Keeper) DeleteConsumerChainId(ctx context.Context, consumerId string) {
	if err := k.ConsumerChainId.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete chain id for consumer id (%s): %w", consumerId, err))
	}
}

// GetConsumerOwnerAddress returns the owner address associated with this consumer id
func (k Keeper) GetConsumerOwnerAddress(ctx context.Context, consumerId string) (string, error) {
	owner, err := k.ConsumerOwnerAddress.Get(ctx, consumerId)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve owner address for consumer id (%s): %w", consumerId, err)
	}
	return owner, nil
}

// SetConsumerOwnerAddress sets the owner address associated with this consumer id
func (k Keeper) SetConsumerOwnerAddress(ctx context.Context, consumerId, owner string) {
	if err := k.ConsumerOwnerAddress.Set(ctx, consumerId, owner); err != nil {
		panic(fmt.Errorf("failed to set owner address for consumer id (%s): %w", consumerId, err))
	}
}

// DeleteConsumerOwnerAddress deletes the owner address associated with this consumer id
func (k Keeper) DeleteConsumerOwnerAddress(ctx context.Context, consumerId string) {
	if err := k.ConsumerOwnerAddress.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete owner address for consumer id (%s): %w", consumerId, err))
	}
}

// GetConsumerMetadata returns the registration record associated with this consumer id
func (k Keeper) GetConsumerMetadata(ctx context.Context, consumerId string) (types.ConsumerMetadata, error) {
	metadata, err := k.ConsumerMetadata.Get(ctx, consumerId)
	if err != nil {
		return types.ConsumerMetadata{}, fmt.Errorf("failed to retrieve metadata for consumer id (%s): %w", consumerId, err)
	}
	return metadata, nil
}

// SetConsumerMetadata sets the registration record associated with this consumer id
func (k Keeper) SetConsumerMetadata(ctx context.Context, consumerId string, metadata types.ConsumerMetadata) error {
	if err := k.ConsumerMetadata.Set(ctx, consumerId, metadata); err != nil {
		return fmt.Errorf("failed to set metadata for consumer id (%s): %w", consumerId, err)
	}
	return nil
}

// DeleteConsumerMetadata deletes the metadata associated with this consumer id
func (k Keeper) DeleteConsumerMetadata(ctx context.Context, consumerId string) {
	if err := k.ConsumerMetadata.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete metadata for consumer id (%s): %w", consumerId, err))
	}
}

// GetConsumerInitializationParameters returns the initialization parameters associated with this consumer id
func (k Keeper) GetConsumerInitializationParameters(ctx context.Context, consumerId string) (types.ConsumerInitializationParameters, error) {
	params, err := k.ConsumerInitParams.Get(ctx, consumerId)
	if err != nil {
		return types.ConsumerInitializationParameters{}, fmt.Errorf("failed to retrieve initialization parameters for consumer id (%s): %w", consumerId, err)
	}
	return params, nil
}

// SetConsumerInitializationParameters sets the initialization parameters associated with this consumer id
func (k Keeper) SetConsumerInitializationParameters(ctx context.Context, consumerId string, parameters types.ConsumerInitializationParameters) error {
	chainId, err := k.GetConsumerChainId(ctx, consumerId)
	if err != nil {
		return fmt.Errorf("failed to get consumer chain ID for consumer id (%s): %w", consumerId, err)
	}
	// validate that the initial height matches the chain ID
	if err := types.ValidateInitialHeight(parameters.InitialHeight, chainId); err != nil {
		return fmt.Errorf("invalid initial height for consumer id (%s): %w", consumerId, err)
	}
	if err := k.ConsumerInitParams.Set(ctx, consumerId, parameters); err != nil {
		return fmt.Errorf("failed to set initialization parameters for consumer id (%s): %w", consumerId, err)
	}
	return nil
}

// DeleteConsumerInitializationParameters deletes the initialization parameters associated with this consumer id
func (k Keeper) DeleteConsumerInitializationParameters(ctx context.Context, consumerId string) {
	if err := k.ConsumerInitParams.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete initialization parameters for consumer id (%s): %w", consumerId, err))
	}
}

// GetConsumerPhase returns the phase associated with this consumer id
func (k Keeper) GetConsumerPhase(ctx context.Context, consumerId string) types.ConsumerPhase {
	phase, err := k.ConsumerPhase.Get(ctx, consumerId)
	if err != nil {
		return types.CONSUMER_PHASE_UNSPECIFIED
	}
	return types.ConsumerPhase(phase)
}

// SetConsumerPhase sets the phase associated with this consumer id
func (k Keeper) SetConsumerPhase(ctx context.Context, consumerId string, phase types.ConsumerPhase) {
	if err := k.ConsumerPhase.Set(ctx, consumerId, uint32(phase)); err != nil {
		panic(fmt.Errorf("failed to set consumer phase for consumer id (%s): %w", consumerId, err))
	}
}

// DeleteConsumerPhase deletes the phase associated with this consumer id
func (k Keeper) DeleteConsumerPhase(ctx context.Context, consumerId string) {
	if err := k.ConsumerPhase.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete consumer phase for consumer id (%s): %w", consumerId, err))
	}
}

// IsConsumerPrelaunched checks if a consumer chain is in its prelaunch phase
func (k Keeper) IsConsumerPrelaunched(ctx context.Context, consumerId string) bool {
	phase := k.GetConsumerPhase(ctx, consumerId)
	return phase == types.CONSUMER_PHASE_REGISTERED ||
		phase == types.CONSUMER_PHASE_INITIALIZED
}

// IsConsumerActive checks if a consumer chain is either registered, initialized, or launched.
func (k Keeper) IsConsumerActive(ctx context.Context, consumerId string) bool {
	phase := k.GetConsumerPhase(ctx, consumerId)
	return phase == types.CONSUMER_PHASE_REGISTERED ||
		phase == types.CONSUMER_PHASE_INITIALIZED ||
		phase == types.CONSUMER_PHASE_LAUNCHED
}
