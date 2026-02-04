package keeper

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"time"

	consumertypes "github.com/allinbits/vaas/x/vaas/consumer/types"
	"github.com/allinbits/vaas/x/vaas/provider/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	ibchost "github.com/cosmos/ibc-go/v10/modules/core/exported"
	ibctmtypes "github.com/cosmos/ibc-go/v10/modules/light-clients/07-tendermint"

	addresscodec "cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Keeper defines the Cross-Chain Validation Provider Keeper
type Keeper struct {
	// address capable of executing gov messages (gov module account)
	authority string

	storeKey storetypes.StoreKey

	cdc codec.BinaryCodec
	// channelKeeper is used for IBC v1 channel-based communication.
	// Deprecated: Will be replaced by IBCPacketHandler in IBC v2.
	channelKeeper vaastypes.ChannelKeeper
	// connectionKeeper is used for IBC v1 connection validation.
	// Deprecated: Connections are managed internally by IBC core in v2.
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

	// ibcPacketHandler is used for IBC v2 (Eureka) client-based packet sending.
	// This field is optional and can be nil if only v1 routing is used.
	// Set via SetIBCPacketHandler after keeper creation.
	ibcPacketHandler vaastypes.IBCPacketHandler
}

// NewKeeper creates a new provider Keeper instance
func NewKeeper(
	cdc codec.BinaryCodec, key storetypes.StoreKey,
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
	k := Keeper{
		cdc:                   cdc,
		storeKey:              key,
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
	}

	k.mustValidateFields()
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

// SetIBCPacketHandler sets the IBC v2 packet handler for client-based routing.
// This method should be called during app wiring if IBC v2 is enabled.
//
// IBC v2 Note: The IBCPacketHandler is optional. If not set, only channel-based
// (v1) routing will be used for packet sending.
func (k *Keeper) SetIBCPacketHandler(handler vaastypes.IBCPacketHandler) {
	k.ibcPacketHandler = handler
}

// Validates that the provider keeper is initialized with non-zero and
// non-nil values for all its fields. Otherwise this method will panic.
func (k Keeper) mustValidateFields() {
	// Ensures no fields are missed in this validation
	// Note: ibcPacketHandler (field 16) is optional and not validated
	if reflect.ValueOf(k).NumField() != 16 {
		panic(fmt.Sprintf("number of fields in provider keeper is not 16 - have %d", reflect.ValueOf(k).NumField()))
	}

	if k.validatorAddressCodec == nil || k.consensusAddressCodec == nil {
		panic("validator and/or consensus address codec are nil")
	}

	vaastypes.PanicIfZeroOrNil(k.cdc, "cdc")                                     // 1
	vaastypes.PanicIfZeroOrNil(k.storeKey, "storeKey")                           // 2
	vaastypes.PanicIfZeroOrNil(k.channelKeeper, "channelKeeper")                 // 4
	vaastypes.PanicIfZeroOrNil(k.connectionKeeper, "connectionKeeper")           // 6
	vaastypes.PanicIfZeroOrNil(k.accountKeeper, "accountKeeper")                 // 7
	vaastypes.PanicIfZeroOrNil(k.clientKeeper, "clientKeeper")                   // 8
	vaastypes.PanicIfZeroOrNil(k.stakingKeeper, "stakingKeeper")                 // 9
	vaastypes.PanicIfZeroOrNil(k.slashingKeeper, "slashingKeeper")               // 10
	vaastypes.PanicIfZeroOrNil(k.distributionKeeper, "distributionKeeper")       // 11
	vaastypes.PanicIfZeroOrNil(k.bankKeeper, "bankKeeper")                       // 12
	vaastypes.PanicIfZeroOrNil(k.feeCollectorName, "feeCollectorName")           // 13
	vaastypes.PanicIfZeroOrNil(k.authority, "authority")                         // 14
	vaastypes.PanicIfZeroOrNil(k.validatorAddressCodec, "validatorAddressCodec") // 15
	vaastypes.PanicIfZeroOrNil(k.consensusAddressCodec, "consensusAddressCodec") // 16

	// this can be nil in tests
	// vaastypes.PanicIfZeroOrNil(k.govKeeper, "govKeeper")                         // 17
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
func (k Keeper) GetPort(ctx sdk.Context) string {
	store := ctx.KVStore(k.storeKey)
	return string(store.Get(types.PortKey()))
}

// SetPort sets the portID for the CCV module. Used in InitGenesis
func (k Keeper) SetPort(ctx sdk.Context, portID string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.PortKey(), []byte(portID))
}

// SetConsumerIdToChannelId sets the mapping from a consumer id to the CCV channel id for that consumer chain.
func (k Keeper) SetConsumerIdToChannelId(ctx sdk.Context, consumerId, channelId string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ConsumerIdToChannelIdKey(consumerId), []byte(channelId))
}

// GetConsumerIdToChannelId gets the CCV channelId for the given consumer id
func (k Keeper) GetConsumerIdToChannelId(ctx sdk.Context, consumerId string) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerIdToChannelIdKey(consumerId))
	if bz == nil {
		return "", false
	}
	return string(bz), true
}

// DeleteConsumerIdToChannelId deletes the CCV channel id for the given consumer id
func (k Keeper) DeleteConsumerIdToChannelId(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerIdToChannelIdKey(consumerId))
}

// GetAllConsumersWithIBCClients returns the ids of all consumer chains that with IBC clients created.
func (k Keeper) GetAllConsumersWithIBCClients(ctx sdk.Context) []string {
	consumerIds := []string{}

	store := ctx.KVStore(k.storeKey)
	iterator := storetypes.KVStorePrefixIterator(store, types.ConsumerIdToClientIdKeyPrefix())
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// remove 1 byte prefix from key to retrieve consumerId
		consumerId := string(iterator.Key()[1:])
		consumerIds = append(consumerIds, consumerId)
	}

	return consumerIds
}

// SetChannelToConsumerId sets the mapping from the CCV channel id to the consumer id.
func (k Keeper) SetChannelToConsumerId(ctx sdk.Context, channelId, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ChannelToConsumerIdKey(channelId), []byte(consumerId))
}

// GetChannelIdToConsumerId gets the consumer id for a given CCV channel id
func (k Keeper) GetChannelIdToConsumerId(ctx sdk.Context, channelID string) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ChannelToConsumerIdKey(channelID))
	if bz == nil {
		return "", false
	}
	return string(bz), true
}

// DeleteChannelIdToConsumerId deletes the consumer id for a given CCV channel id
func (k Keeper) DeleteChannelIdToConsumerId(ctx sdk.Context, channelId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ChannelToConsumerIdKey(channelId))
}

// GetAllChannelToConsumers gets all channel to chain mappings. If a mapping exists,
// then the CCV channel to that consumer chain is established.
//
// Note that mapping from CCV channel IDs to consumer IDs
// is stored under keys with the following format:
// ChannelIdToConsumerIdKeyPrefix | channelID
// Thus, the returned array is in ascending order of channelIDs.
func (k Keeper) GetAllChannelToConsumers(ctx sdk.Context) (channelsToConsumers []struct {
	ChannelId  string
	ConsumerId string
},
) {
	store := ctx.KVStore(k.storeKey)
	iterator := storetypes.KVStorePrefixIterator(store, types.ChannelIdToConsumerIdKeyPrefix())
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// remove prefix from key to retrieve channelID
		channelID := string(iterator.Key()[1:])
		consumerId := string(iterator.Value())

		channelsToConsumers = append(channelsToConsumers, struct {
			ChannelId  string
			ConsumerId string
		}{
			ChannelId:  channelID,
			ConsumerId: consumerId,
		})
	}

	return channelsToConsumers
}

func (k Keeper) SetConsumerGenesis(ctx sdk.Context, consumerId string, gen vaastypes.ConsumerGenesisState) error {
	store := ctx.KVStore(k.storeKey)
	bz, err := gen.Marshal()
	if err != nil {
		return err
	}
	store.Set(types.ConsumerGenesisKey(consumerId), bz)

	return nil
}

func (k Keeper) GetConsumerGenesis(ctx sdk.Context, consumerId string) (vaastypes.ConsumerGenesisState, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ConsumerGenesisKey(consumerId))
	if bz == nil {
		return vaastypes.ConsumerGenesisState{}, false
	}

	var data vaastypes.ConsumerGenesisState
	if err := data.Unmarshal(bz); err != nil {
		// An error here would indicate something is very wrong,
		// the ConsumerGenesis is assumed to be correctly serialized in SetConsumerGenesis.
		panic(fmt.Errorf("consumer genesis could not be unmarshaled: %w", err))
	}
	return data, true
}

func (k Keeper) DeleteConsumerGenesis(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ConsumerGenesisKey(consumerId))
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
// If there is already a CCV channel between the provider and consumer
// chain then close the channel, so that another channel can be made.
//
// SetConsumerChain is called by OnChanOpenConfirm.
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

func (k Keeper) IncrementValidatorSetUpdateId(ctx sdk.Context) {
	validatorSetUpdateId := k.GetValidatorSetUpdateId(ctx)
	k.SetValidatorSetUpdateId(ctx, validatorSetUpdateId+1)
}

func (k Keeper) SetValidatorSetUpdateId(ctx sdk.Context, valUpdateID uint64) {
	store := ctx.KVStore(k.storeKey)

	// Convert back into bytes for storage
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, valUpdateID)

	store.Set(types.ValidatorSetUpdateIdKey(), bz)
}

func (k Keeper) GetValidatorSetUpdateId(ctx sdk.Context) (validatorSetUpdateId uint64) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ValidatorSetUpdateIdKey())

	if bz == nil {
		return 0
	}
	return binary.BigEndian.Uint64(bz)
}

// SetValsetUpdateBlockHeight sets the block height for a given valset update id
func (k Keeper) SetValsetUpdateBlockHeight(ctx sdk.Context, valsetUpdateId, blockHeight uint64) {
	store := ctx.KVStore(k.storeKey)
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, blockHeight)
	store.Set(types.ValsetUpdateBlockHeightKey(valsetUpdateId), heightBytes)
}

// GetValsetUpdateBlockHeight gets the block height for a given valset update id
func (k Keeper) GetValsetUpdateBlockHeight(ctx sdk.Context, valsetUpdateId uint64) (uint64, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.ValsetUpdateBlockHeightKey(valsetUpdateId))
	if bz == nil {
		return 0, false
	}
	return binary.BigEndian.Uint64(bz), true
}

// GetAllValsetUpdateBlockHeights gets all the block heights for all valset updates
//
// Note that the mapping from vscIDs to block heights is stored under keys with the following format:
// ValsetUpdateBlockHeightKeyPrefix | vscID
// Thus, the returned array is in ascending order of vscIDs.
func (k Keeper) GetAllValsetUpdateBlockHeights(ctx sdk.Context) (valsetUpdateBlockHeights []types.ValsetUpdateIdToHeight) {
	store := ctx.KVStore(k.storeKey)
	iterator := storetypes.KVStorePrefixIterator(store, types.ValsetUpdateBlockHeightKeyPrefix())

	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		valsetUpdateId := binary.BigEndian.Uint64(iterator.Key()[1:])
		height := binary.BigEndian.Uint64(iterator.Value())

		valsetUpdateBlockHeights = append(valsetUpdateBlockHeights, types.ValsetUpdateIdToHeight{
			ValsetUpdateId: valsetUpdateId,
			Height:         height,
		})
	}

	return valsetUpdateBlockHeights
}

// DeleteValsetUpdateBlockHeight deletes the block height value for a given vaset update id
func (k Keeper) DeleteValsetUpdateBlockHeight(ctx sdk.Context, valsetUpdateId uint64) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ValsetUpdateBlockHeightKey(valsetUpdateId))
}

// SetInitChainHeight sets the provider block height when the given consumer chain was initiated
func (k Keeper) SetInitChainHeight(ctx sdk.Context, consumerId string, height uint64) {
	store := ctx.KVStore(k.storeKey)
	heightBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBytes, height)

	store.Set(types.InitChainHeightKey(consumerId), heightBytes)
}

// GetInitChainHeight returns the provider block height when the given consumer chain was initiated
func (k Keeper) GetInitChainHeight(ctx sdk.Context, consumerId string) (uint64, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.InitChainHeightKey(consumerId))
	if bz == nil {
		return 0, false
	}

	return binary.BigEndian.Uint64(bz), true
}

// DeleteInitChainHeight deletes the block height value for which the given consumer chain's channel was established
func (k Keeper) DeleteInitChainHeight(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.InitChainHeightKey(consumerId))
}

// GetPendingVSCPackets returns the list of pending ValidatorSetChange packets stored under consumer id
func (k Keeper) GetPendingVSCPackets(ctx sdk.Context, consumerId string) []vaastypes.ValidatorSetChangePacketData {
	var packets types.ValidatorSetChangePackets

	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.PendingVSCsKey(consumerId))
	if bz == nil {
		return []vaastypes.ValidatorSetChangePacketData{}
	}
	if err := packets.Unmarshal(bz); err != nil {
		// An error here would indicate something is very wrong,
		// the PendingVSCPackets are assumed to be correctly serialized in AppendPendingVSCPackets.
		panic(fmt.Errorf("cannot unmarshal pending validator set changes: %w", err))
	}
	return packets.GetList()
}

// AppendPendingVSCPackets adds the given ValidatorSetChange packet to the list
// of pending ValidatorSetChange packets stored under consumer id
func (k Keeper) AppendPendingVSCPackets(ctx sdk.Context, consumerId string, newPackets ...vaastypes.ValidatorSetChangePacketData) {
	pds := append(k.GetPendingVSCPackets(ctx, consumerId), newPackets...)

	store := ctx.KVStore(k.storeKey)
	packets := types.ValidatorSetChangePackets{List: pds}
	buf, err := packets.Marshal()
	if err != nil {
		// An error here would indicate something is very wrong,
		// packets is instantiated in this method and should be able to be marshaled.
		panic(fmt.Errorf("cannot marshal pending validator set changes: %w", err))
	}
	store.Set(types.PendingVSCsKey(consumerId), buf)
}

// DeletePendingVSCPackets deletes the list of pending ValidatorSetChange packets for chain ID
func (k Keeper) DeletePendingVSCPackets(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.PendingVSCsKey(consumerId))
}

// SetConsumerClientId sets the client id for the given consumer id.
// Note that the method also stores a reverse index that can be accessed
// by calling GetClientIdToConsumerId.
func (k Keeper) SetConsumerClientId(ctx sdk.Context, consumerId, clientId string) {
	store := ctx.KVStore(k.storeKey)

	if prevClientId, found := k.GetConsumerClientId(ctx, consumerId); found {
		// delete reverse index
		store.Delete(types.ClientIdToConsumerIdKey(prevClientId))
	}

	store.Set(types.ConsumerIdToClientIdKey(consumerId), []byte(clientId))

	// set the reverse index
	store.Set(types.ClientIdToConsumerIdKey(clientId), []byte(consumerId))
}

// GetConsumerClientId returns the client id for the given consumer id.
func (k Keeper) GetConsumerClientId(ctx sdk.Context, consumerId string) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	clientIdBytes := store.Get(types.ConsumerIdToClientIdKey(consumerId))
	if clientIdBytes == nil {
		return "", false
	}
	return string(clientIdBytes), true
}

// GetClientIdToConsumerId returns the consumer id associated with this client id
func (k Keeper) GetClientIdToConsumerId(ctx sdk.Context, clientId string) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	consumerIdBytes := store.Get(types.ClientIdToConsumerIdKey(clientId))
	if consumerIdBytes == nil {
		return "", false
	}
	return string(consumerIdBytes), true
}

// DeleteConsumerClientId removes from the store the client id for the given consumer id.
// Note that the method also removes the reverse index.
func (k Keeper) DeleteConsumerClientId(ctx sdk.Context, consumerId string) {
	store := ctx.KVStore(k.storeKey)

	if clientId, found := k.GetConsumerClientId(ctx, consumerId); found {
		// delete reverse index
		store.Delete(types.ClientIdToConsumerIdKey(clientId))
	}

	store.Delete(types.ConsumerIdToClientIdKey(consumerId))
}

func (k Keeper) BondDenom(ctx sdk.Context) (string, error) {
	return k.stakingKeeper.BondDenom(ctx)
}

// GetAllConsumerIds returns all the existing consumer ids
func (k Keeper) GetAllConsumerIds(ctx sdk.Context) []string {
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
func (k Keeper) GetAllActiveConsumerIds(ctx sdk.Context) []string {
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
