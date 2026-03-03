package keeper

import (
	"context"
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/provider/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"
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

	// Option A: If connection_id is provided, validate the IBC link is fully established
	// This is for sovereign chains migrating to VAAS validation where an existing IBC
	// connection must already be functional (connection OPEN, counterparty registered, client active)
	if parameters.ConnectionId != "" {
		if err := k.validateExistingIBCLink(ctx, parameters.ConnectionId, chainId); err != nil {
			return fmt.Errorf("invalid connection_id for consumer id (%s): %w", consumerId, err)
		}
	}

	if err := k.ConsumerInitParams.Set(ctx, consumerId, parameters); err != nil {
		return fmt.Errorf("failed to set initialization parameters for consumer id (%s): %w", consumerId, err)
	}
	return nil
}

// validateExistingIBCLink validates that an existing IBC connection is fully functional.
// This is called when connection_id is provided (Option A: sovereign chain migration).
// For the IBC link to be valid:
//  1. Connection must exist and be in OPEN state
//  2. Counterparty connection must be set (bidirectional link established)
//  3. Associated client must be a Tendermint client matching the expected chain ID
//  4. Client must be active (not frozen or expired)
//
// In IBC v2 (Eureka) terminology, this validates that RegisterCounterparty has been
// successfully called on both chains and the link is ready for packet routing.
func (k Keeper) validateExistingIBCLink(ctx context.Context, connectionId, expectedChainId string) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// 1. Get the connection and verify it exists
	connectionEnd, found := k.connectionKeeper.GetConnection(sdkCtx, connectionId)
	if !found {
		return fmt.Errorf("connection not found: %s", connectionId)
	}

	// 2. Verify connection is in OPEN state (handshake complete)
	if connectionEnd.State != conntypes.OPEN {
		return fmt.Errorf("connection %s is not in OPEN state (current: %s), IBC link not fully established",
			connectionId, connectionEnd.State.String())
	}

	// 3. Verify counterparty connection is set (bidirectional link)
	if connectionEnd.Counterparty.ConnectionId == "" {
		return fmt.Errorf("connection %s has no counterparty connection ID, IBC link not bidirectional", connectionId)
	}

	// 4. Get and validate the client
	clientId := connectionEnd.ClientId
	clientStateI, found := k.clientKeeper.GetClientState(sdkCtx, clientId)
	if !found {
		return fmt.Errorf("client %s associated with connection %s not found", clientId, connectionId)
	}

	// 5. Verify it's a Tendermint client
	tmClient, ok := clientStateI.(*ibctmtypes.ClientState)
	if !ok {
		return fmt.Errorf("client %s is not a Tendermint client (type: %s)", clientId, clientStateI.ClientType())
	}

	// 6. Verify the chain ID matches the expected consumer chain
	if tmClient.ChainId != expectedChainId {
		return fmt.Errorf("client %s chain ID mismatch: expected %s, got %s",
			clientId, expectedChainId, tmClient.ChainId)
	}

	// 7. Verify client is not frozen
	if tmClient.FrozenHeight.RevisionHeight != 0 {
		return fmt.Errorf("client %s is frozen at height %s, IBC link not functional",
			clientId, tmClient.FrozenHeight.String())
	}

	// 8. Verify client is not expired by checking if the trusting period has elapsed
	// Get the latest consensus state to check the timestamp
	consensusState, found := k.clientKeeper.GetLatestClientConsensusState(sdkCtx, clientId)
	if !found {
		return fmt.Errorf("no consensus state found for client %s", clientId)
	}

	// The consensus state timestamp plus trusting period must be after the current time
	// for the client to still be within its trusting period
	tmConsensusState, ok := consensusState.(*ibctmtypes.ConsensusState)
	if !ok {
		return fmt.Errorf("consensus state for client %s is not a Tendermint consensus state", clientId)
	}

	expirationTime := tmConsensusState.Timestamp.Add(tmClient.TrustingPeriod)
	if sdkCtx.BlockTime().After(expirationTime) {
		return fmt.Errorf("client %s has expired (last update: %s, trusting period: %s, current time: %s), IBC link not functional",
			clientId, tmConsensusState.Timestamp.String(), tmClient.TrustingPeriod.String(), sdkCtx.BlockTime().String())
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
