package keeper

import (
	"context"
	"fmt"
	"time"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmprotocrypto "github.com/cometbft/cometbft/proto/tendermint/crypto"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	ibchost "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	"cosmossdk.io/collections"
	"cosmossdk.io/collections/indexes"
	addresscodec "cosmossdk.io/core/address"
	corestoretypes "cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	paramtypes "github.com/cosmos/cosmos-sdk/x/params/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Keeper defines the Cross-Chain Validation Provider Keeper
type Keeper struct {
	// address capable of executing gov messages (gov module account)
	authority string

	storeService corestoretypes.KVStoreService

	cdc                codec.BinaryCodec
	channelKeeper      vaastypes.ChannelKeeper
	connectionKeeper   vaastypes.ConnectionKeeper
	accountKeeper      vaastypes.AccountKeeper
	clientKeeper       vaastypes.ClientKeeper
	stakingKeeper      vaastypes.StakingKeeper
	slashingKeeper     vaastypes.SlashingKeeper
	distributionKeeper vaastypes.DistributionKeeper
	bankKeeper         vaastypes.BankKeeper
	govKeeper          govkeeper.Keeper
	feeCollectorName   string

	validatorAddressCodec addresscodec.Codec
	consensusAddressCodec addresscodec.Codec

	// Collections schema
	Schema collections.Schema

	// State collections
	Port                          collections.Item[string]
	Params                        collections.Item[types.Params]
	ValidatorSetUpdateId          collections.Sequence
	ConsumerId                    collections.Sequence
	ConsumerChannels              *collections.IndexedMap[string, string, ConsumerChannelIndexes]
	ConsumerClients               *collections.IndexedMap[string, string, ConsumerClientIndexes]
	ConsumerGenesis               collections.Map[string, vaastypes.ConsumerGenesisState]
	ValsetUpdateBlockHeight       collections.Map[uint64, uint64]
	InitChainHeight               collections.Map[string, uint64]
	PendingVSCPackets             collections.Map[string, types.ValidatorSetChangePackets]
	ConsumerChainId               collections.Map[string, string]
	ConsumerOwnerAddress          collections.Map[string, string]
	ConsumerMetadata              collections.Map[string, types.ConsumerMetadata]
	ConsumerInitParams            collections.Map[string, types.ConsumerInitializationParameters]
	ConsumerPhase                 collections.Map[string, uint32]
	EquivocationEvidenceMinHeight collections.Map[string, uint64]
	ConsumerRemovalTime           collections.Map[string, []byte]
	SpawnTimeToConsumerIds        collections.Map[[]byte, types.ConsumerIds]
	RemovalTimeToConsumerIds      collections.Map[[]byte, types.ConsumerIds]

	// Key assignment collections
	ValidatorConsumerPubKey collections.Map[collections.Pair[string, []byte], []byte]
	ValidatorByConsumerAddr collections.Map[collections.Pair[string, []byte], []byte]
	ConsumerAddrsToPrune    collections.Map[collections.Pair[string, []byte], types.AddressList]

	// Validator set collections
	ConsumerValidators        collections.Map[collections.Pair[string, []byte], types.ConsensusValidator]
	LastProviderConsensusVals collections.Map[[]byte, types.ConsensusValidator]
}

// NewKeeper creates a new provider Keeper instance
func NewKeeper(
	cdc codec.BinaryCodec, storeService corestoretypes.KVStoreService, paramSpace paramtypes.Subspace,
	channelKeeper vaastypes.ChannelKeeper,
	connectionKeeper vaastypes.ConnectionKeeper, clientKeeper vaastypes.ClientKeeper,
	stakingKeeper vaastypes.StakingKeeper, slashingKeeper vaastypes.SlashingKeeper,
	accountKeeper vaastypes.AccountKeeper,
	distributionKeeper vaastypes.DistributionKeeper, bankKeeper vaastypes.BankKeeper,
	govKeeper govkeeper.Keeper,
	authority string,
	validatorAddressCodec, consensusAddressCodec addresscodec.Codec,
	feeCollectorName string,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		cdc:                   cdc,
		storeService:          storeService,
		authority:             authority,
		channelKeeper:         channelKeeper,
		connectionKeeper:      connectionKeeper,
		clientKeeper:          clientKeeper,
		stakingKeeper:         stakingKeeper,
		slashingKeeper:        slashingKeeper,
		accountKeeper:         accountKeeper,
		distributionKeeper:    distributionKeeper,
		bankKeeper:            bankKeeper,
		feeCollectorName:      feeCollectorName,
		validatorAddressCodec: validatorAddressCodec,
		consensusAddressCodec: consensusAddressCodec,
		govKeeper:             govKeeper,

		// Initialize collections
		Port:                 collections.NewItem(sb, types.PortPrefix, "port", collections.StringValue),
		Params:               collections.NewItem(sb, types.ParametersPrefix, "params", codec.CollValue[types.Params](cdc)),
		ValidatorSetUpdateId: collections.NewSequence(sb, types.ValidatorSetUpdateIdPrefix, "validator_set_update_id"),
		ConsumerId:           collections.NewSequence(sb, types.ConsumerIdPrefix, "consumer_id"),
		ConsumerChannels: collections.NewIndexedMap(
			sb,
			types.ConsumerIdToChannelIdPrefix,
			"consumer_channels",
			collections.StringKey,
			collections.StringValue,
			ConsumerChannelIndexes{
				ByChannelId: indexes.NewUnique(
					sb,
					types.ChannelIdToConsumerIdPrefix,
					"consumer_channels_by_channel_id",
					collections.StringKey,
					collections.StringKey,
					func(_ string, channelId string) (string, error) {
						return channelId, nil
					},
				),
			},
		),
		ConsumerClients: collections.NewIndexedMap(
			sb,
			types.ConsumerIdToClientIdPrefix,
			"consumer_clients",
			collections.StringKey,
			collections.StringValue,
			ConsumerClientIndexes{
				ByClientId: indexes.NewUnique(
					sb,
					types.ClientIdToConsumerIdPrefix,
					"consumer_clients_by_client_id",
					collections.StringKey,
					collections.StringKey,
					func(_ string, clientId string) (string, error) {
						return clientId, nil
					},
				),
			},
		),
		ConsumerGenesis:               collections.NewMap(sb, types.ConsumerGenesisPrefix, "consumer_genesis", collections.StringKey, codec.CollValue[vaastypes.ConsumerGenesisState](cdc)),
		ValsetUpdateBlockHeight:       collections.NewMap(sb, types.ValsetUpdateBlockHeightPrefix, "valset_update_block_height", collections.Uint64Key, collections.Uint64Value),
		InitChainHeight:               collections.NewMap(sb, types.InitChainHeightPrefix, "init_chain_height", collections.StringKey, collections.Uint64Value),
		PendingVSCPackets:             collections.NewMap(sb, types.PendingVSCsPrefix, "pending_vsc_packets", collections.StringKey, codec.CollValue[types.ValidatorSetChangePackets](cdc)),
		ConsumerChainId:               collections.NewMap(sb, types.ConsumerIdToChainIdPrefix, "consumer_chain_id", collections.StringKey, collections.StringValue),
		ConsumerOwnerAddress:          collections.NewMap(sb, types.ConsumerIdToOwnerAddressPrefix, "consumer_owner_address", collections.StringKey, collections.StringValue),
		ConsumerMetadata:              collections.NewMap(sb, types.ConsumerIdToMetadataPrefix, "consumer_metadata", collections.StringKey, codec.CollValue[types.ConsumerMetadata](cdc)),
		ConsumerInitParams:            collections.NewMap(sb, types.ConsumerIdToInitializationParamsPrefix, "consumer_init_params", collections.StringKey, codec.CollValue[types.ConsumerInitializationParameters](cdc)),
		ConsumerPhase:                 collections.NewMap(sb, types.ConsumerIdToPhasePrefix, "consumer_phase", collections.StringKey, collections.Uint32Value),
		EquivocationEvidenceMinHeight: collections.NewMap(sb, types.EquivocationEvidenceMinHeightPrefix, "equivocation_evidence_min_height", collections.StringKey, collections.Uint64Value),
		ConsumerRemovalTime:           collections.NewMap(sb, types.ConsumerIdToRemovalTimePrefix, "consumer_removal_time", collections.StringKey, collections.BytesValue),
		SpawnTimeToConsumerIds:        collections.NewMap(sb, types.SpawnTimeToConsumerIdsPrefix, "spawn_time_to_consumer_ids", collections.BytesKey, codec.CollValue[types.ConsumerIds](cdc)),
		RemovalTimeToConsumerIds:      collections.NewMap(sb, types.RemovalTimeToConsumerIdsPrefix, "removal_time_to_consumer_ids", collections.BytesKey, codec.CollValue[types.ConsumerIds](cdc)),

		// Key assignment collections
		ValidatorConsumerPubKey: collections.NewMap(sb, types.ConsumerValidatorsPrefix, "validator_consumer_pub_key", collections.PairKeyCodec(collections.StringKey, collections.BytesKey), collections.BytesValue),
		ValidatorByConsumerAddr: collections.NewMap(sb, types.ValidatorsByConsumerAddrPrefix, "validator_by_consumer_addr", collections.PairKeyCodec(collections.StringKey, collections.BytesKey), collections.BytesValue),
		ConsumerAddrsToPrune:    collections.NewMap(sb, types.ConsumerAddrsToPruneV2Prefix, "consumer_addrs_to_prune", collections.PairKeyCodec(collections.StringKey, collections.BytesKey), codec.CollValue[types.AddressList](cdc)),

		// Validator set collections
		ConsumerValidators:        collections.NewMap(sb, types.ConsumerValidatorPrefix, "consumer_validators", collections.PairKeyCodec(collections.StringKey, collections.BytesKey), codec.CollValue[types.ConsensusValidator](cdc)),
		LastProviderConsensusVals: collections.NewMap(sb, types.LastProviderConsensusVals, "last_provider_consensus_vals", collections.BytesKey, codec.CollValue[types.ConsensusValidator](cdc)),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// GetAuthority returns the x/ccv/provider module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// ValidatorAddressCodec returns the app validator address codec.
func (k Keeper) ValidatorAddressCodec() addresscodec.Codec {
	return k.validatorAddressCodec
}

// ConsensusAddressCodec returns the app consensus address codec.
func (k Keeper) ConsensusAddressCodec() addresscodec.Codec {
	return k.consensusAddressCodec
}

func (k *Keeper) SetGovKeeper(govKeeper govkeeper.Keeper) {
	k.govKeeper = govKeeper
}

// Logger returns a module-specific logger.
func (k Keeper) Logger(ctx context.Context) log.Logger {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return sdkCtx.Logger().With("module", "x/"+ibchost.ModuleName+"-"+types.ModuleName)
}

// GetPort returns the portID for the CCV module. Used in ExportGenesis
func (k Keeper) GetPort(ctx context.Context) string {
	port, err := k.Port.Get(ctx)
	if err != nil {
		return ""
	}
	return port
}

// SetPort sets the portID for the CCV module. Used in InitGenesis
func (k Keeper) SetPort(ctx context.Context, portID string) {
	if err := k.Port.Set(ctx, portID); err != nil {
		panic(fmt.Errorf("failed to set port: %w", err))
	}
}

// SetConsumerIdToChannelId sets the mapping from a consumer id to the CCV channel id for that consumer chain.
func (k Keeper) SetConsumerIdToChannelId(ctx context.Context, consumerId, channelId string) {
	if err := k.ConsumerChannels.Set(ctx, consumerId, channelId); err != nil {
		panic(fmt.Errorf("failed to set consumer id to channel id: %w", err))
	}
}

// GetConsumerIdToChannelId gets the CCV channelId for the given consumer id
func (k Keeper) GetConsumerIdToChannelId(ctx context.Context, consumerId string) (string, bool) {
	channelId, err := k.ConsumerChannels.Get(ctx, consumerId)
	if err != nil {
		return "", false
	}
	return channelId, true
}

// DeleteConsumerIdToChannelId deletes the CCV channel id for the given consumer id
func (k Keeper) DeleteConsumerIdToChannelId(ctx context.Context, consumerId string) {
	if err := k.ConsumerChannels.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete consumer id to channel id: %w", err))
	}
}

// GetAllConsumersWithIBCClients returns the ids of all consumer chains that with IBC clients created.
func (k Keeper) GetAllConsumersWithIBCClients(ctx context.Context) []string {
	consumerIds := []string{}

	iter, err := k.ConsumerClients.Iterate(ctx, nil)
	if err != nil {
		return consumerIds
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		consumerIds = append(consumerIds, key)
	}

	return consumerIds
}

// SetChannelToConsumerId sets the mapping from the CCV channel id to the consumer id.
// Note: This is now handled automatically by the ConsumerChannels indexed map.
// This method is kept for API compatibility but the index is maintained automatically.
func (k Keeper) SetChannelToConsumerId(ctx context.Context, channelId, consumerId string) {
	// The reverse index is automatically maintained by ConsumerChannels IndexedMap
	// This method exists for backwards compatibility
	if err := k.ConsumerChannels.Set(ctx, consumerId, channelId); err != nil {
		panic(fmt.Errorf("failed to set channel to consumer id: %w", err))
	}
}

// GetChannelIdToConsumerId gets the consumer id for a given CCV channel id
func (k Keeper) GetChannelIdToConsumerId(ctx context.Context, channelID string) (string, bool) {
	consumerId, err := k.ConsumerChannels.Indexes.ByChannelId.MatchExact(ctx, channelID)
	if err != nil {
		return "", false
	}
	return consumerId, true
}

// DeleteChannelIdToConsumerId deletes the consumer id for a given CCV channel id
// Note: This is now handled automatically by the ConsumerChannels indexed map.
func (k Keeper) DeleteChannelIdToConsumerId(ctx context.Context, channelId string) {
	// Look up the consumerId first, then remove from the indexed map
	consumerId, err := k.ConsumerChannels.Indexes.ByChannelId.MatchExact(ctx, channelId)
	if err != nil {
		return // Not found, nothing to delete
	}
	if err := k.ConsumerChannels.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete channel id to consumer id: %w", err))
	}
}

// GetAllChannelToConsumers gets all channel to chain mappings.
// The results are sorted by ChannelId.
func (k Keeper) GetAllChannelToConsumers(ctx context.Context) (channelsToConsumers []struct {
	ChannelId  string
	ConsumerId string
},
) {
	// Iterate over the ByChannelId index to get results sorted by ChannelId
	iter, err := k.ConsumerChannels.Indexes.ByChannelId.Iterate(ctx, nil)
	if err != nil {
		return channelsToConsumers
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		// FullKey returns Pair[ReferenceKey, PrimaryKey] = Pair[ChannelId, ConsumerId]
		fullKey, err := iter.FullKey()
		if err != nil {
			continue
		}
		channelId := fullKey.K1()
		consumerId := fullKey.K2()
		channelsToConsumers = append(channelsToConsumers, struct {
			ChannelId  string
			ConsumerId string
		}{
			ChannelId:  channelId,
			ConsumerId: consumerId,
		})
	}

	return channelsToConsumers
}

func (k Keeper) SetConsumerGenesis(ctx context.Context, consumerId string, gen vaastypes.ConsumerGenesisState) error {
	return k.ConsumerGenesis.Set(ctx, consumerId, gen)
}

func (k Keeper) GetConsumerGenesis(ctx context.Context, consumerId string) (vaastypes.ConsumerGenesisState, bool) {
	gen, err := k.ConsumerGenesis.Get(ctx, consumerId)
	if err != nil {
		return vaastypes.ConsumerGenesisState{}, false
	}
	return gen, true
}

func (k Keeper) DeleteConsumerGenesis(ctx context.Context, consumerId string) {
	if err := k.ConsumerGenesis.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete consumer genesis: %w", err))
	}
}

// VerifyConsumerChain verifies that the chain trying to connect on the channel handshake
// is the expected consumer chain.
func (k Keeper) VerifyConsumerChain(ctx sdk.Context, channelID string, connectionHops []string) error {
	if len(connectionHops) != 1 {
		return errorsmod.Wrap(channeltypes.ErrTooManyConnectionHops, "must have direct connection to provider chain")
	}
	connectionID := connectionHops[0]
	clientId, _, err := k.getUnderlyingClient(ctx, connectionID)
	if err != nil {
		return err
	}

	consumerId, found := k.GetClientIdToConsumerId(ctx, clientId)
	if !found {
		return errorsmod.Wrapf(vaastypes.ErrConsumerChainNotFound, "cannot find consumer id associated with client id: %s", clientId)
	}
	ccvClientId, found := k.GetConsumerClientId(ctx, consumerId)
	if !found {
		return errorsmod.Wrapf(vaastypes.ErrClientNotFound, "cannot find client for consumer chain %s", consumerId)
	}
	if ccvClientId != clientId {
		return errorsmod.Wrapf(types.ErrInvalidConsumerClient, "CCV channel must be built on top of CCV client. expected %s, got %s", ccvClientId, clientId)
	}

	// Verify that there isn't already a CCV channel for the consumer chain
	if prevChannel, ok := k.GetConsumerIdToChannelId(ctx, consumerId); ok {
		return errorsmod.Wrapf(vaastypes.ErrDuplicateChannel, "CCV channel with ID: %s already created for consumer chain %s", prevChannel, consumerId)
	}
	return nil
}

// SetConsumerChain ensures that the consumer chain has not already been
// set by a different channel, and then sets the consumer chain mappings
// in keeper, and set the channel status to validating.
func (k Keeper) SetConsumerChain(ctx sdk.Context, channelID string) error {
	channel, ok := k.channelKeeper.GetChannel(ctx, vaastypes.ProviderPortID, channelID)
	if !ok {
		return errorsmod.Wrapf(channeltypes.ErrChannelNotFound, "channel not found for channel ID: %s", channelID)
	}
	if len(channel.ConnectionHops) != 1 {
		return errorsmod.Wrap(channeltypes.ErrTooManyConnectionHops, "must have direct connection to consumer chain")
	}
	connectionID := channel.ConnectionHops[0]
	clientID, tmClient, err := k.getUnderlyingClient(ctx, connectionID)
	if err != nil {
		return err
	}
	consumerId, found := k.GetClientIdToConsumerId(ctx, clientID)
	if !found {
		return errorsmod.Wrapf(types.ErrNoConsumerId, "cannot find a consumer chain associated for this client: %s", clientID)
	}
	// Verify that there isn't already a CCV channel for the consumer chain
	chainID := tmClient.ChainId
	if prevChannelID, ok := k.GetConsumerIdToChannelId(ctx, consumerId); ok {
		return errorsmod.Wrapf(vaastypes.ErrDuplicateChannel, "CCV channel with ID: %s already created for consumer chain with id %s", prevChannelID, consumerId)
	}

	// the CCV channel is established:
	// - set channel mappings
	k.SetConsumerIdToChannelId(ctx, consumerId, channelID)
	k.SetChannelToConsumerId(ctx, channelID, consumerId)
	// - set current block height for the consumer chain initialization
	k.SetInitChainHeight(ctx, consumerId, uint64(ctx.BlockHeight()))

	// emit event on successful addition
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			vaastypes.EventTypeChannelEstablished,
			sdk.NewAttribute(sdk.AttributeKeyModule, consumertypes.ModuleName),
			sdk.NewAttribute(types.AttributeConsumerId, consumerId),
			sdk.NewAttribute(types.AttributeConsumerChainId, chainID),
			sdk.NewAttribute(conntypes.AttributeKeyClientID, clientID),
			sdk.NewAttribute(channeltypes.AttributeKeyChannelID, channelID),
			sdk.NewAttribute(conntypes.AttributeKeyConnectionID, connectionID),
		),
	)
	return nil
}

// Retrieves the underlying client state corresponding to a connection ID.
func (k Keeper) getUnderlyingClient(ctx sdk.Context, connectionID string) (
	clientID string, tmClient *ibctmtypes.ClientState, err error,
) {
	conn, ok := k.connectionKeeper.GetConnection(ctx, connectionID)
	if !ok {
		return "", nil, errorsmod.Wrapf(conntypes.ErrConnectionNotFound,
			"connection not found for connection ID: %s", connectionID)
	}
	clientID = conn.ClientId
	clientState, ok := k.clientKeeper.GetClientState(ctx, clientID)
	if !ok {
		return "", nil, errorsmod.Wrapf(clienttypes.ErrClientNotFound,
			"client not found for client ID: %s", conn.ClientId)
	}
	tmClient, ok = clientState.(*ibctmtypes.ClientState)
	if !ok {
		return "", nil, errorsmod.Wrapf(clienttypes.ErrInvalidClientType,
			"invalid client type. expected %s, got %s", ibchost.Tendermint, clientState.ClientType())
	}
	return clientID, tmClient, nil
}

// chanCloseInit defines a wrapper function for the channel Keeper's function
func (k Keeper) chanCloseInit(ctx sdk.Context, channelID string) error {
	return k.channelKeeper.ChanCloseInit(ctx, vaastypes.ProviderPortID, channelID)
}

func (k Keeper) IncrementValidatorSetUpdateId(ctx context.Context) {
	validatorSetUpdateId := k.GetValidatorSetUpdateId(ctx)
	k.SetValidatorSetUpdateId(ctx, validatorSetUpdateId+1)
}

func (k Keeper) SetValidatorSetUpdateId(ctx context.Context, valUpdateID uint64) {
	if err := k.ValidatorSetUpdateId.Set(ctx, valUpdateID); err != nil {
		panic(fmt.Errorf("failed to set validator set update id: %w", err))
	}
}

func (k Keeper) GetValidatorSetUpdateId(ctx context.Context) (validatorSetUpdateId uint64) {
	val, err := k.ValidatorSetUpdateId.Peek(ctx)
	if err != nil {
		return 0
	}
	return val
}

// SetValsetUpdateBlockHeight sets the block height for a given valset update id
func (k Keeper) SetValsetUpdateBlockHeight(ctx context.Context, valsetUpdateId, blockHeight uint64) {
	if err := k.ValsetUpdateBlockHeight.Set(ctx, valsetUpdateId, blockHeight); err != nil {
		panic(fmt.Errorf("failed to set valset update block height: %w", err))
	}
}

// GetValsetUpdateBlockHeight gets the block height for a given valset update id
func (k Keeper) GetValsetUpdateBlockHeight(ctx context.Context, valsetUpdateId uint64) (uint64, bool) {
	height, err := k.ValsetUpdateBlockHeight.Get(ctx, valsetUpdateId)
	if err != nil {
		return 0, false
	}
	return height, true
}

// GetAllValsetUpdateBlockHeights gets all the block heights for all valset updates
func (k Keeper) GetAllValsetUpdateBlockHeights(ctx context.Context) (valsetUpdateBlockHeights []types.ValsetUpdateIdToHeight) {
	iter, err := k.ValsetUpdateBlockHeight.Iterate(ctx, nil)
	if err != nil {
		return valsetUpdateBlockHeights
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		valsetUpdateBlockHeights = append(valsetUpdateBlockHeights, types.ValsetUpdateIdToHeight{
			ValsetUpdateId: kv.Key,
			Height:         kv.Value,
		})
	}

	return valsetUpdateBlockHeights
}

// DeleteValsetUpdateBlockHeight deletes the block height value for a given vaset update id
func (k Keeper) DeleteValsetUpdateBlockHeight(ctx context.Context, valsetUpdateId uint64) {
	if err := k.ValsetUpdateBlockHeight.Remove(ctx, valsetUpdateId); err != nil {
		panic(fmt.Errorf("failed to delete valset update block height: %w", err))
	}
}

// SetInitChainHeight sets the provider block height when the given consumer chain was initiated
func (k Keeper) SetInitChainHeight(ctx context.Context, consumerId string, height uint64) {
	if err := k.InitChainHeight.Set(ctx, consumerId, height); err != nil {
		panic(fmt.Errorf("failed to set init chain height: %w", err))
	}
}

// GetInitChainHeight returns the provider block height when the given consumer chain was initiated
func (k Keeper) GetInitChainHeight(ctx context.Context, consumerId string) (uint64, bool) {
	height, err := k.InitChainHeight.Get(ctx, consumerId)
	if err != nil {
		return 0, false
	}
	return height, true
}

// DeleteInitChainHeight deletes the block height value for which the given consumer chain's channel was established
func (k Keeper) DeleteInitChainHeight(ctx context.Context, consumerId string) {
	if err := k.InitChainHeight.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete init chain height: %w", err))
	}
}

// GetPendingVSCPackets returns the list of pending ValidatorSetChange packets stored under consumer id
func (k Keeper) GetPendingVSCPackets(ctx context.Context, consumerId string) []vaastypes.ValidatorSetChangePacketData {
	packets, err := k.PendingVSCPackets.Get(ctx, consumerId)
	if err != nil {
		return []vaastypes.ValidatorSetChangePacketData{}
	}
	return packets.GetList()
}

// AppendPendingVSCPackets adds the given ValidatorSetChange packet to the list
// of pending ValidatorSetChange packets stored under consumer id
func (k Keeper) AppendPendingVSCPackets(ctx context.Context, consumerId string, newPackets ...vaastypes.ValidatorSetChangePacketData) {
	pds := append(k.GetPendingVSCPackets(ctx, consumerId), newPackets...)

	packets := types.ValidatorSetChangePackets{List: pds}
	if err := k.PendingVSCPackets.Set(ctx, consumerId, packets); err != nil {
		panic(fmt.Errorf("failed to append pending VSC packets: %w", err))
	}
}

// DeletePendingVSCPackets deletes the list of pending ValidatorSetChange packets for chain ID
func (k Keeper) DeletePendingVSCPackets(ctx context.Context, consumerId string) {
	if err := k.PendingVSCPackets.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete pending VSC packets: %w", err))
	}
}

// SetConsumerClientId sets the client id for the given consumer id.
// The reverse index is automatically maintained by the indexed map.
func (k Keeper) SetConsumerClientId(ctx context.Context, consumerId, clientId string) {
	if err := k.ConsumerClients.Set(ctx, consumerId, clientId); err != nil {
		panic(fmt.Errorf("failed to set consumer id to client id: %w", err))
	}
}

// GetConsumerClientId returns the client id for the given consumer id.
func (k Keeper) GetConsumerClientId(ctx context.Context, consumerId string) (string, bool) {
	clientId, err := k.ConsumerClients.Get(ctx, consumerId)
	if err != nil {
		return "", false
	}
	return clientId, true
}

// GetClientIdToConsumerId returns the consumer id associated with this client id
func (k Keeper) GetClientIdToConsumerId(ctx context.Context, clientId string) (string, bool) {
	consumerId, err := k.ConsumerClients.Indexes.ByClientId.MatchExact(ctx, clientId)
	if err != nil {
		return "", false
	}
	return consumerId, true
}

// DeleteConsumerClientId removes from the store the client id for the given consumer id.
// The reverse index is automatically cleaned up by the indexed map.
func (k Keeper) DeleteConsumerClientId(ctx context.Context, consumerId string) {
	if err := k.ConsumerClients.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to remove consumer id to client id mapping: %w", err))
	}
}

func (k Keeper) BondDenom(ctx sdk.Context) (string, error) {
	return k.stakingKeeper.BondDenom(ctx)
}

// GetAllConsumerIds returns all the existing consumer ids
func (k Keeper) GetAllConsumerIds(ctx context.Context) []string {
	latestConsumerId, found := k.GetConsumerId(ctx)
	if !found {
		return []string{}
	}

	consumerIds := []string{}
	for i := uint64(0); i < latestConsumerId; i++ {
		consumerId := fmt.Sprintf("%d", i)
		consumerIds = append(consumerIds, consumerId)
	}

	return consumerIds
}

// GetAllActiveConsumerIds returns all the consumer ids of chains that are registered, initialized, or launched
func (k Keeper) GetAllActiveConsumerIds(ctx context.Context) []string {
	consumerIds := []string{}
	for _, consumerId := range k.GetAllConsumerIds(ctx) {
		if !k.IsConsumerActive(ctx, consumerId) {
			continue
		}
		consumerIds = append(consumerIds, consumerId)
	}
	return consumerIds
}

func (k Keeper) UnbondingCanComplete(ctx sdk.Context, id uint64) error {
	return k.stakingKeeper.UnbondingCanComplete(ctx, id)
}

func (k Keeper) UnbondingTime(ctx sdk.Context) (time.Duration, error) {
	return k.stakingKeeper.UnbondingTime(ctx)
}

// CreateProviderConsensusValidator creates a new ConsensusValidator from the given staking validator
func (k Keeper) CreateProviderConsensusValidator(ctx sdk.Context, val stakingtypes.Validator) (types.ConsensusValidator, error) {
	consAddr, err := val.GetConsAddr()
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("getting consensus address: %w", err)
	}
	pubKey, err := val.CmtConsPublicKey()
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("getting consensus public key: %w", err)
	}
	valAddr, err := sdk.ValAddressFromBech32(val.GetOperator())
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("getting validator address: %w", err)
	}

	power, err := k.stakingKeeper.GetLastValidatorPower(ctx, valAddr)
	if err != nil {
		return types.ConsensusValidator{}, fmt.Errorf("getting validator power: %w", err)
	}

	return types.ConsensusValidator{
		ProviderConsAddr: consAddr,
		PublicKey:        &pubKey,
		Power:            power,
	}, nil
}

// SetEquivocationEvidenceMinHeight sets the minimum height for a valid consumer equivocation evidence
func (k Keeper) SetEquivocationEvidenceMinHeight(ctx context.Context, consumerId string, height uint64) {
	if err := k.EquivocationEvidenceMinHeight.Set(ctx, consumerId, height); err != nil {
		panic(fmt.Errorf("failed to set equivocation evidence min height: %w", err))
	}
}

// GetEquivocationEvidenceMinHeight returns the minimum height for a valid consumer equivocation evidence
func (k Keeper) GetEquivocationEvidenceMinHeight(ctx context.Context, consumerId string) uint64 {
	height, err := k.EquivocationEvidenceMinHeight.Get(ctx, consumerId)
	if err != nil {
		return 0
	}
	return height
}

// DeleteEquivocationEvidenceMinHeight deletes the minimum height for a valid consumer equivocation evidence
func (k Keeper) DeleteEquivocationEvidenceMinHeight(ctx context.Context, consumerId string) {
	if err := k.EquivocationEvidenceMinHeight.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete equivocation evidence min height: %w", err))
	}
}

// SetConsumerRemovalTime sets the removal time associated with this consumer id
func (k Keeper) SetConsumerRemovalTime(ctx context.Context, consumerId string, removalTime time.Time) error {
	buf, err := removalTime.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal removal time (%+v) for consumer id (%s): %w", removalTime, consumerId, err)
	}
	if err := k.ConsumerRemovalTime.Set(ctx, consumerId, buf); err != nil {
		return fmt.Errorf("failed to set removal time for consumer id (%s): %w", consumerId, err)
	}
	return nil
}

// GetConsumerRemovalTime returns the removal time associated with the to-be-removed chain with consumer id
func (k Keeper) GetConsumerRemovalTime(ctx context.Context, consumerId string) (time.Time, error) {
	buf, err := k.ConsumerRemovalTime.Get(ctx, consumerId)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to retrieve removal time for consumer id (%s): %w", consumerId, err)
	}
	var t time.Time
	if err := t.UnmarshalBinary(buf); err != nil {
		return time.Time{}, fmt.Errorf("failed to unmarshal removal time for consumer id (%s): %w", consumerId, err)
	}
	return t, nil
}

// DeleteConsumerRemovalTime deletes the removal time associated with this consumer id
func (k Keeper) DeleteConsumerRemovalTime(ctx context.Context, consumerId string) {
	if err := k.ConsumerRemovalTime.Remove(ctx, consumerId); err != nil {
		panic(fmt.Errorf("failed to delete removal time for consumer id (%s): %w", consumerId, err))
	}
}

// timeToBytes converts a time.Time to bytes for use as a key
func timeToBytes(t time.Time) []byte {
	bz, _ := t.MarshalBinary()
	return bz
}

// bytesToTime converts bytes back to time.Time
func bytesToTime(bz []byte) (time.Time, error) {
	var t time.Time
	if err := t.UnmarshalBinary(bz); err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// GetConsumersToBeLaunched returns all the consumer ids of chains stored under this spawn time
func (k Keeper) GetConsumersToBeLaunched(ctx context.Context, spawnTime time.Time) (types.ConsumerIds, error) {
	key := timeToBytes(spawnTime)
	consumerIds, err := k.SpawnTimeToConsumerIds.Get(ctx, key)
	if err != nil {
		return types.ConsumerIds{}, nil
	}
	return consumerIds, nil
}

// AppendConsumerToBeLaunched appends the provider consumer id for the given spawn time
func (k Keeper) AppendConsumerToBeLaunched(ctx context.Context, consumerId string, spawnTime time.Time) error {
	consumers, err := k.GetConsumersToBeLaunched(ctx, spawnTime)
	if err != nil {
		return err
	}

	consumersWithAppend := types.ConsumerIds{
		Ids: append(consumers.Ids, consumerId),
	}

	key := timeToBytes(spawnTime)
	return k.SpawnTimeToConsumerIds.Set(ctx, key, consumersWithAppend)
}

// RemoveConsumerToBeLaunched removes consumer id from if stored for this specific spawn time
func (k Keeper) RemoveConsumerToBeLaunched(ctx context.Context, consumerId string, spawnTime time.Time) error {
	consumers, err := k.GetConsumersToBeLaunched(ctx, spawnTime)
	if err != nil {
		return err
	}

	if len(consumers.Ids) == 0 {
		return fmt.Errorf("no consumer ids found for this time: %s", spawnTime.String())
	}

	// find the index of the consumer we want to remove
	index := -1
	for i := 0; i < len(consumers.Ids); i++ {
		if consumers.Ids[i] == consumerId {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("failed to find consumer id (%s)", consumerId)
	}

	key := timeToBytes(spawnTime)
	if len(consumers.Ids) == 1 {
		return k.SpawnTimeToConsumerIds.Remove(ctx, key)
	}

	consumersWithRemoval := types.ConsumerIds{
		Ids: append(consumers.Ids[:index], consumers.Ids[index+1:]...),
	}

	return k.SpawnTimeToConsumerIds.Set(ctx, key, consumersWithRemoval)
}

// DeleteAllConsumersToBeLaunched deletes all consumer to be launched at this specific spawn time
func (k Keeper) DeleteAllConsumersToBeLaunched(ctx context.Context, spawnTime time.Time) {
	key := timeToBytes(spawnTime)
	if err := k.SpawnTimeToConsumerIds.Remove(ctx, key); err != nil {
		panic(fmt.Errorf("failed to delete consumers to be launched: %w", err))
	}
}

// GetConsumersToBeRemoved returns all the consumer ids of chains stored under this removal time
func (k Keeper) GetConsumersToBeRemoved(ctx context.Context, removalTime time.Time) (types.ConsumerIds, error) {
	key := timeToBytes(removalTime)
	consumerIds, err := k.RemovalTimeToConsumerIds.Get(ctx, key)
	if err != nil {
		return types.ConsumerIds{}, nil
	}
	return consumerIds, nil
}

// AppendConsumerToBeRemoved appends the provider consumer id for the given removal time
func (k Keeper) AppendConsumerToBeRemoved(ctx context.Context, consumerId string, removalTime time.Time) error {
	consumers, err := k.GetConsumersToBeRemoved(ctx, removalTime)
	if err != nil {
		return err
	}

	consumersWithAppend := types.ConsumerIds{
		Ids: append(consumers.Ids, consumerId),
	}

	key := timeToBytes(removalTime)
	return k.RemovalTimeToConsumerIds.Set(ctx, key, consumersWithAppend)
}

// RemoveConsumerToBeRemoved removes consumer id from the given removal time
func (k Keeper) RemoveConsumerToBeRemoved(ctx context.Context, consumerId string, removalTime time.Time) error {
	consumers, err := k.GetConsumersToBeRemoved(ctx, removalTime)
	if err != nil {
		return err
	}

	if len(consumers.Ids) == 0 {
		return fmt.Errorf("no consumer ids found for this time: %s", removalTime.String())
	}

	// find the index of the consumer we want to remove
	index := -1
	for i := 0; i < len(consumers.Ids); i++ {
		if consumers.Ids[i] == consumerId {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("failed to find consumer id (%s)", consumerId)
	}

	key := timeToBytes(removalTime)
	if len(consumers.Ids) == 1 {
		return k.RemovalTimeToConsumerIds.Remove(ctx, key)
	}

	consumersWithRemoval := types.ConsumerIds{
		Ids: append(consumers.Ids[:index], consumers.Ids[index+1:]...),
	}

	return k.RemovalTimeToConsumerIds.Set(ctx, key, consumersWithRemoval)
}

// DeleteAllConsumersToBeRemoved deletes all consumer to be removed at this specific removal time
func (k Keeper) DeleteAllConsumersToBeRemoved(ctx context.Context, removalTime time.Time) {
	key := timeToBytes(removalTime)
	if err := k.RemovalTimeToConsumerIds.Remove(ctx, key); err != nil {
		panic(fmt.Errorf("failed to delete consumers to be removed: %w", err))
	}
}

// Key assignment methods using collections

// GetValidatorConsumerPubKey returns a validator's public key assigned for a consumer chain
func (k Keeper) GetValidatorConsumerPubKey(
	ctx context.Context,
	consumerId string,
	providerAddr types.ProviderConsAddress,
) (consumerKey tmprotocrypto.PublicKey, found bool) {
	bz, err := k.ValidatorConsumerPubKey.Get(ctx, collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes()))
	if err != nil {
		return consumerKey, false
	}
	if err := consumerKey.Unmarshal(bz); err != nil {
		panic(fmt.Sprintf("failed to unmarshal consumer key: %v", err))
	}
	return consumerKey, true
}

// SetValidatorConsumerPubKey sets a validator's public key assigned for a consumer chain
func (k Keeper) SetValidatorConsumerPubKey(
	ctx context.Context,
	consumerId string,
	providerAddr types.ProviderConsAddress,
	consumerKey tmprotocrypto.PublicKey,
) {
	bz, err := consumerKey.Marshal()
	if err != nil {
		panic(fmt.Sprintf("failed to marshal consumer key: %v", err))
	}
	if err := k.ValidatorConsumerPubKey.Set(ctx, collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes()), bz); err != nil {
		panic(fmt.Errorf("failed to set validator consumer pub key: %w", err))
	}
}

// DeleteValidatorConsumerPubKey deletes a validator's public key assigned for a consumer chain
func (k Keeper) DeleteValidatorConsumerPubKey(ctx context.Context, consumerId string, providerAddr types.ProviderConsAddress) {
	if err := k.ValidatorConsumerPubKey.Remove(ctx, collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes())); err != nil {
		panic(fmt.Errorf("failed to delete validator consumer pub key: %w", err))
	}
}

// GetAllValidatorConsumerPubKeys gets all the validators public keys assigned for a consumer chain
func (k Keeper) GetAllValidatorConsumerPubKeys(ctx context.Context, consumerId *string) (validatorConsumerPubKeys []types.ValidatorConsumerPubKey) {
	var iter collections.Iterator[collections.Pair[string, []byte], []byte]
	var err error

	if consumerId == nil {
		iter, err = k.ValidatorConsumerPubKey.Iterate(ctx, nil)
	} else {
		iter, err = k.ValidatorConsumerPubKey.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](*consumerId))
	}
	if err != nil {
		return validatorConsumerPubKeys
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		var consumerKey tmprotocrypto.PublicKey
		if err := consumerKey.Unmarshal(kv.Value); err != nil {
			panic(fmt.Sprintf("failed to unmarshal consumer key: %v", err))
		}
		validatorConsumerPubKeys = append(validatorConsumerPubKeys, types.ValidatorConsumerPubKey{
			ChainId:      kv.Key.K1(),
			ProviderAddr: kv.Key.K2(),
			ConsumerKey:  &consumerKey,
		})
	}

	return validatorConsumerPubKeys
}

// GetValidatorByConsumerAddr returns a validator's consensus address on the provider
// given the validator's consensus address on a consumer
func (k Keeper) GetValidatorByConsumerAddr(
	ctx context.Context,
	consumerId string,
	consumerAddr types.ConsumerConsAddress,
) (providerAddr types.ProviderConsAddress, found bool) {
	bz, err := k.ValidatorByConsumerAddr.Get(ctx, collections.Join(consumerId, consumerAddr.ToSdkConsAddr().Bytes()))
	if err != nil {
		return providerAddr, false
	}
	providerAddr = types.NewProviderConsAddress(bz)
	return providerAddr, true
}

// SetValidatorByConsumerAddr sets the mapping from a validator's consensus address on a consumer
// to the validator's consensus address on the provider
func (k Keeper) SetValidatorByConsumerAddr(
	ctx context.Context,
	consumerId string,
	consumerAddr types.ConsumerConsAddress,
	providerAddr types.ProviderConsAddress,
) {
	if err := k.ValidatorByConsumerAddr.Set(ctx, collections.Join(consumerId, consumerAddr.ToSdkConsAddr().Bytes()), providerAddr.ToSdkConsAddr().Bytes()); err != nil {
		panic(fmt.Errorf("failed to set validator by consumer addr: %w", err))
	}
}

// DeleteValidatorByConsumerAddr deletes the mapping from a validator's consensus address on a consumer
// to the validator's consensus address on the provider
func (k Keeper) DeleteValidatorByConsumerAddr(ctx context.Context, consumerId string, consumerAddr types.ConsumerConsAddress) {
	if err := k.ValidatorByConsumerAddr.Remove(ctx, collections.Join(consumerId, consumerAddr.ToSdkConsAddr().Bytes())); err != nil {
		panic(fmt.Errorf("failed to delete validator by consumer addr: %w", err))
	}
}

// GetAllValidatorsByConsumerAddr gets all the mappings from consensus addresses on consumer chains
// to consensus addresses on the provider chain
func (k Keeper) GetAllValidatorsByConsumerAddr(ctx context.Context, consumerId *string) (validatorConsumerAddrs []types.ValidatorByConsumerAddr) {
	var iter collections.Iterator[collections.Pair[string, []byte], []byte]
	var err error

	if consumerId == nil {
		iter, err = k.ValidatorByConsumerAddr.Iterate(ctx, nil)
	} else {
		iter, err = k.ValidatorByConsumerAddr.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](*consumerId))
	}
	if err != nil {
		return validatorConsumerAddrs
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		consumerAddr := types.NewConsumerConsAddress(kv.Key.K2())
		providerAddr := types.NewProviderConsAddress(kv.Value)

		validatorConsumerAddrs = append(validatorConsumerAddrs, types.ValidatorByConsumerAddr{
			ConsumerAddr: consumerAddr.ToSdkConsAddr(),
			ProviderAddr: providerAddr.ToSdkConsAddr(),
			ChainId:      kv.Key.K1(),
		})
	}

	return validatorConsumerAddrs
}

// AppendConsumerAddrsToPrune appends a consumer validator address to the list of consumer addresses
// that can be pruned once the block time is at least pruneTs.
func (k Keeper) AppendConsumerAddrsToPrune(
	ctx context.Context,
	consumerId string,
	pruneTs time.Time,
	consumerAddr types.ConsumerConsAddress,
) {
	key := collections.Join(consumerId, timeToBytes(pruneTs))
	addrList, err := k.ConsumerAddrsToPrune.Get(ctx, key)
	if err != nil {
		addrList = types.AddressList{}
	}
	addrList.Addresses = append(addrList.Addresses, consumerAddr.ToSdkConsAddr())
	if err := k.ConsumerAddrsToPrune.Set(ctx, key, addrList); err != nil {
		panic(fmt.Errorf("failed to append consumer addrs to prune: %w", err))
	}
}

// GetConsumerAddrsToPrune returns the list of consumer addresses to prune stored under timestamp ts.
func (k Keeper) GetConsumerAddrsToPrune(
	ctx context.Context,
	consumerId string,
	ts time.Time,
) (consumerAddrsToPrune types.AddressList) {
	key := collections.Join(consumerId, timeToBytes(ts))
	addrList, err := k.ConsumerAddrsToPrune.Get(ctx, key)
	if err != nil {
		return types.AddressList{}
	}
	return addrList
}

// DeleteConsumerAddrsToPrune deletes the list of consumer addresses mapped to a timestamp
func (k Keeper) DeleteConsumerAddrsToPrune(ctx context.Context, consumerId string, pruneTs time.Time) {
	key := collections.Join(consumerId, timeToBytes(pruneTs))
	if err := k.ConsumerAddrsToPrune.Remove(ctx, key); err != nil {
		panic(fmt.Errorf("failed to delete consumer addrs to prune: %w", err))
	}
}

// GetAllConsumerAddrsToPrune gets all consumer addresses that can be eventually pruned for a given consumerId.
func (k Keeper) GetAllConsumerAddrsToPrune(ctx context.Context, consumerId string) (consumerAddrsToPrune []types.ConsumerAddrsToPruneV2) {
	iter, err := k.ConsumerAddrsToPrune.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](consumerId))
	if err != nil {
		return consumerAddrsToPrune
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		ts, err := bytesToTime(kv.Key.K2())
		if err != nil {
			continue
		}
		consumerAddrsToPrune = append(consumerAddrsToPrune, types.ConsumerAddrsToPruneV2{
			PruneTs:       ts,
			ConsumerAddrs: &kv.Value,
			ChainId:       consumerId,
		})
	}

	return consumerAddrsToPrune
}

// ConsumeConsumerAddrsToPrune returns the list of consumer addresses that can be pruned at timestamp ts.
// The returned addresses are removed from the store.
func (k Keeper) ConsumeConsumerAddrsToPrune(
	ctx context.Context,
	consumerId string,
	ts time.Time,
) (consumerAddrsToPrune types.AddressList) {
	iter, err := k.ConsumerAddrsToPrune.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](consumerId))
	if err != nil {
		return consumerAddrsToPrune
	}
	defer iter.Close()

	var keysToDel []collections.Pair[string, []byte]
	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		pruneTs, err := bytesToTime(kv.Key.K2())
		if err != nil {
			k.Logger(ctx).Error("bytesToTime failed", "error", err.Error())
			continue
		}
		if pruneTs.After(ts) {
			continue
		}

		keysToDel = append(keysToDel, kv.Key)
		consumerAddrsToPrune.Addresses = append(consumerAddrsToPrune.Addresses, kv.Value.Addresses...)
	}

	for _, key := range keysToDel {
		if err := k.ConsumerAddrsToPrune.Remove(ctx, key); err != nil {
			k.Logger(ctx).Error("failed to delete consumer addr to prune", "error", err.Error())
		}
	}

	return consumerAddrsToPrune
}

// Validator set storage methods using collections

// SetConsumerValidator sets provided consumer `validator` on the consumer chain with `consumerId`
func (k Keeper) SetConsumerValidator(
	ctx context.Context,
	consumerId string,
	validator types.ConsensusValidator,
) error {
	return k.ConsumerValidators.Set(ctx, collections.Join(consumerId, validator.ProviderConsAddr), validator)
}

// GetConsumerValidator returns the consumer validator with `providerAddr` if it exists for chain with `consumerId`
func (k Keeper) GetConsumerValidator(ctx context.Context, consumerId string, providerAddr types.ProviderConsAddress) (types.ConsensusValidator, bool) {
	validator, err := k.ConsumerValidators.Get(ctx, collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes()))
	if err != nil {
		return types.ConsensusValidator{}, false
	}
	return validator, true
}

// DeleteConsumerValidator removes consumer validator with `providerAddr` address
func (k Keeper) DeleteConsumerValidator(
	ctx context.Context,
	consumerId string,
	providerConsAddr types.ProviderConsAddress,
) {
	if err := k.ConsumerValidators.Remove(ctx, collections.Join(consumerId, providerConsAddr.ToSdkConsAddr().Bytes())); err != nil {
		panic(fmt.Errorf("failed to delete consumer validator: %w", err))
	}
}

// IsConsumerValidator returns `true` if the consumer validator with `providerAddr` exists for chain with `consumerId`
func (k Keeper) IsConsumerValidator(ctx context.Context, consumerId string, providerAddr types.ProviderConsAddress) bool {
	has, err := k.ConsumerValidators.Has(ctx, collections.Join(consumerId, providerAddr.ToSdkConsAddr().Bytes()))
	if err != nil {
		return false
	}
	return has
}

// GetConsumerValSet returns all the consumer validators for chain with `consumerId`
func (k Keeper) GetConsumerValSet(
	ctx context.Context,
	consumerId string,
) ([]types.ConsensusValidator, error) {
	var validators []types.ConsensusValidator
	iter, err := k.ConsumerValidators.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](consumerId))
	if err != nil {
		return validators, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			return validators, err
		}
		validators = append(validators, val)
	}

	return validators, nil
}

// SetConsumerValSet resets the current consumer validators with the `nextValidators`
func (k Keeper) SetConsumerValSet(ctx context.Context, consumerId string, nextValidators []types.ConsensusValidator) error {
	// First delete existing validators
	k.DeleteConsumerValSet(ctx, consumerId)

	// Then set new validators
	for _, val := range nextValidators {
		if err := k.SetConsumerValidator(ctx, consumerId, val); err != nil {
			return err
		}
	}
	return nil
}

// DeleteConsumerValSet deletes all the stored consumer validators for chain with `consumerId`
func (k Keeper) DeleteConsumerValSet(ctx context.Context, consumerId string) {
	iter, err := k.ConsumerValidators.Iterate(ctx, collections.NewPrefixedPairRange[string, []byte](consumerId))
	if err != nil {
		return
	}
	defer iter.Close()

	var keysToDel []collections.Pair[string, []byte]
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		keysToDel = append(keysToDel, key)
	}

	for _, key := range keysToDel {
		if err := k.ConsumerValidators.Remove(ctx, key); err != nil {
			panic(fmt.Errorf("failed to delete consumer validator: %w", err))
		}
	}
}

// SetLastProviderConsensusValSet resets the stored last validator set sent to the consensus engine on the provider
// to the provided next validators.
func (k Keeper) SetLastProviderConsensusValSet(ctx context.Context, nextValidators []types.ConsensusValidator) error {
	// First delete existing validators
	iter, err := k.LastProviderConsensusVals.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	var keysToDel [][]byte
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			continue
		}
		keysToDel = append(keysToDel, key)
	}
	iter.Close()

	for _, key := range keysToDel {
		if err := k.LastProviderConsensusVals.Remove(ctx, key); err != nil {
			return err
		}
	}

	// Then set new validators
	for _, val := range nextValidators {
		if err := k.LastProviderConsensusVals.Set(ctx, val.ProviderConsAddr, val); err != nil {
			return err
		}
	}
	return nil
}

// GetLastProviderConsensusValSet returns the last stored validator set sent to
// the consensus engine on the provider
func (k Keeper) GetLastProviderConsensusValSet(ctx context.Context) ([]types.ConsensusValidator, error) {
	var validators []types.ConsensusValidator
	iter, err := k.LastProviderConsensusVals.Iterate(ctx, nil)
	if err != nil {
		return validators, err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			return validators, err
		}
		validators = append(validators, val)
	}

	return validators, nil
}

// NewKeeperWithStoreKey creates a provider keeper using a store key (for backwards compatibility in tests)
func NewKeeperWithStoreKey(
	cdc codec.BinaryCodec, storeService corestoretypes.KVStoreService,
	channelKeeper vaastypes.ChannelKeeper,
	connectionKeeper vaastypes.ConnectionKeeper, clientKeeper vaastypes.ClientKeeper,
	stakingKeeper vaastypes.StakingKeeper, slashingKeeper vaastypes.SlashingKeeper,
	accountKeeper vaastypes.AccountKeeper,
	distributionKeeper vaastypes.DistributionKeeper, bankKeeper vaastypes.BankKeeper,
	govKeeper govkeeper.Keeper,
	authority string,
	validatorAddressCodec, consensusAddressCodec addresscodec.Codec,
	feeCollectorName string,
) Keeper {
	return NewKeeper(
		cdc, storeService, paramtypes.Subspace{},
		channelKeeper, connectionKeeper, clientKeeper,
		stakingKeeper, slashingKeeper, accountKeeper,
		distributionKeeper, bankKeeper, govKeeper,
		authority, validatorAddressCodec, consensusAddressCodec, feeCollectorName,
	)
}
