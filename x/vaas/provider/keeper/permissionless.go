package keeper

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/allinbits/vaas/x/vaas/provider/types"

	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// setConsumerId sets the provided consumerId
func (k Keeper) setConsumerId(ctx sdk.Context, consumerId uint64) {
	store := ctx.KVStore(k.storeKey)

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, consumerId)

	store.Set(types.ConsumerIdKey(), buf)
}

// GetConsumerId returns the next to-be-assigned consumer id
func (k Keeper) GetConsumerId(ctx sdk.Context) (uint64, bool) {
	store := ctx.KVStore(k.storeKey)
	buf := store.Get(types.ConsumerIdKey())
	if buf == nil {
		return 0, false
	}
	return binary.BigEndian.Uint64(buf), true
}

// FetchAndIncrementConsumerId fetches the first consumer id that can be used and increments the
// underlying consumer id
func (k Keeper) FetchAndIncrementConsumerId(ctx sdk.Context) string {
	consumerId, _ := k.GetConsumerId(ctx)
	k.setConsumerId(ctx, consumerId+1)
	return strconv.FormatUint(consumerId, 10)
}

// GetConsumerChainId returns the chain id associated with this consumer id
func (k Keeper) GetConsumerChainId(ctx sdk.Context, consumerId string) (string, error) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerIdToChainIdKey(consumerId))
	if bz == nil {
		return "", fmt.Errorf("failed to retrieve chain id for consumer id (%s)", consumerId)
	}
	return string(bz), nil
}

// SetConsumerChainId sets the chain id associated with this consumer id
func (k Keeper) SetConsumerChainId(ctx sdk.Context, consumerId, chainId string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ConsumerIdToChainIdKey(consumerId), []byte(chainId))
}

// DeleteConsumerChainId deletes the chain id associated with this consumer id
func (k Keeper) DeleteConsumerChainId(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToChainIdKey(consumerId))
}

// GetConsumerOwnerAddress returns the owner address associated with this consumer id
func (k Keeper) GetConsumerOwnerAddress(ctx sdk.Context, consumerId string) (string, error) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerIdToOwnerAddressKey(consumerId))
	if bz == nil {
		return "", fmt.Errorf("failed to retrieve owner address for consumer id (%s)", consumerId)
	}
	return string(bz), nil
}

// SetConsumerOwnerAddress sets the owner address associated with this consumer id
func (k Keeper) SetConsumerOwnerAddress(ctx sdk.Context, consumerId, owner string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ConsumerIdToOwnerAddressKey(consumerId), []byte(owner))
}

// DeleteConsumerOwnerAddress deletes the owner address associated with this consumer id
func (k Keeper) DeleteConsumerOwnerAddress(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToOwnerAddressKey(consumerId))
}

// GetConsumerMetadata returns the registration record associated with this consumer id
func (k Keeper) GetConsumerMetadata(ctx sdk.Context, consumerId string) (types.ConsumerMetadata, error) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerIdToMetadataKey(consumerId))
	if bz == nil {
		return types.ConsumerMetadata{}, fmt.Errorf("failed to retrieve metadata for consumer id (%s)", consumerId)
	}
	var metadata types.ConsumerMetadata
	if err := metadata.Unmarshal(bz); err != nil {
		return types.ConsumerMetadata{}, fmt.Errorf("failed to unmarshal metadata for consumer id (%s): %w", consumerId, err)
	}
	return metadata, nil
}

// SetConsumerMetadata sets the registration record associated with this consumer id
func (k Keeper) SetConsumerMetadata(ctx sdk.Context, consumerId string, metadata types.ConsumerMetadata) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := metadata.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal registration metadata (%+v) for consumer id (%s): %w", metadata, consumerId, err)
	}
	store.Set(types.ConsumerIdToMetadataKey(consumerId), bz)
	return nil
}

// DeleteConsumerMetadata deletes the metadata associated with this consumer id
func (k Keeper) DeleteConsumerMetadata(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToMetadataKey(consumerId))
}

// GetConsumerInitializationParameters returns the initialization parameters associated with this consumer id
func (k Keeper) GetConsumerInitializationParameters(ctx sdk.Context, consumerId string) (types.ConsumerInitializationParameters, error) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerIdToInitializationParametersKey(consumerId))
	if bz == nil {
		return types.ConsumerInitializationParameters{}, fmt.Errorf("failed to retrieve initialization parameters for consumer id (%s)", consumerId)
	}
	var initializationParameters types.ConsumerInitializationParameters
	if err := initializationParameters.Unmarshal(bz); err != nil {
		return types.ConsumerInitializationParameters{}, fmt.Errorf("failed to unmarshal initialization parameters for consumer id (%s): %w", consumerId, err)
	}
	return initializationParameters, nil
}

// SetConsumerInitializationParameters sets the initialization parameters associated with this consumer id
func (k Keeper) SetConsumerInitializationParameters(ctx sdk.Context, consumerId string, parameters types.ConsumerInitializationParameters) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := parameters.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal initialization parameters (%+v) for consumer id (%s): %w", parameters, consumerId, err)
	}
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

	store.Set(types.ConsumerIdToInitializationParametersKey(consumerId), bz)
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
func (k Keeper) validateExistingIBCLink(ctx sdk.Context, connectionId, expectedChainId string) error {
	// 1. Get the connection and verify it exists
	connectionEnd, found := k.connectionKeeper.GetConnection(ctx, connectionId)
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
	clientStateI, found := k.clientKeeper.GetClientState(ctx, clientId)
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
	consensusState, found := k.clientKeeper.GetLatestClientConsensusState(ctx, clientId)
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
	if ctx.BlockTime().After(expirationTime) {
		return fmt.Errorf("client %s has expired (last update: %s, trusting period: %s, current time: %s), IBC link not functional",
			clientId, tmConsensusState.Timestamp.String(), tmClient.TrustingPeriod.String(), ctx.BlockTime().String())
	}

	return nil
}

// DeleteConsumerInitializationParameters deletes the initialization parameters associated with this consumer id
func (k Keeper) DeleteConsumerInitializationParameters(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToInitializationParametersKey(consumerId))
}

// GetConsumerPhase returns the phase associated with this consumer id
func (k Keeper) GetConsumerPhase(ctx sdk.Context, consumerId string) types.ConsumerPhase {
	store := ctx.KVStore(k.storeKey)
	buf := store.Get(types.ConsumerIdToPhaseKey(consumerId))
	if buf == nil {
		return types.CONSUMER_PHASE_UNSPECIFIED
	}
	phase := types.ConsumerPhase(binary.BigEndian.Uint32(buf))
	return phase
}

// SetConsumerPhase sets the phase associated with this consumer id
func (k Keeper) SetConsumerPhase(ctx sdk.Context, consumerId string, phase types.ConsumerPhase) {
	store := ctx.KVStore(k.storeKey)
	phaseBytes := make([]byte, 8)
	binary.BigEndian.PutUint32(phaseBytes, uint32(phase))
	store.Set(types.ConsumerIdToPhaseKey(consumerId), phaseBytes)
}

// DeleteConsumerPhase deletes the phase associated with this consumer id
func (k Keeper) DeleteConsumerPhase(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToPhaseKey(consumerId))
}

// IsConsumerPrelaunched checks if a consumer chain is in its prelaunch phase
func (k Keeper) IsConsumerPrelaunched(ctx sdk.Context, consumerId string) bool {
	phase := k.GetConsumerPhase(ctx, consumerId)
	return phase == types.CONSUMER_PHASE_REGISTERED ||
		phase == types.CONSUMER_PHASE_INITIALIZED
}

// IsConsumerActive checks if a consumer chain is either registered, initialized, or launched.
func (k Keeper) IsConsumerActive(ctx sdk.Context, consumerId string) bool {
	phase := k.GetConsumerPhase(ctx, consumerId)
	return phase == types.CONSUMER_PHASE_REGISTERED ||
		phase == types.CONSUMER_PHASE_INITIALIZED ||
		phase == types.CONSUMER_PHASE_LAUNCHED
}
