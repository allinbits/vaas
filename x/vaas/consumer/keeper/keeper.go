package keeper

import (
	"context"
	"fmt"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	host "github.com/cosmos/ibc-go/v10/modules/core/24-host"

	"cosmossdk.io/collections"
	addresscodec "cosmossdk.io/core/address"
	corestoretypes "cosmossdk.io/core/store"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Keeper defines the Cross-Chain Validation Consumer Keeper
type Keeper struct {
	// the address capable of executing a MsgUpdateParams message. Typically, this
	// should be the x/gov module account.
	authority string

	storeService     corestoretypes.KVStoreService
	cdc              codec.BinaryCodec
	channelKeeper    vaastypes.ChannelKeeper
	connectionKeeper vaastypes.ConnectionKeeper
	clientKeeper     vaastypes.ClientKeeper
	// standaloneStakingKeeper is the staking keeper that managed proof of stake for a previously standalone chain,
	// before the chain went through a standalone to consumer changeover.
	// This keeper is not used for consumers that launched with ICS, and is therefore set after the constructor.
	standaloneStakingKeeper vaastypes.StakingKeeper
	slashingKeeper          vaastypes.SlashingKeeper
	hooks                   vaastypes.ConsumerHooks
	bankKeeper              vaastypes.BankKeeper
	authKeeper              vaastypes.AccountKeeper
	ibcTransferKeeper       vaastypes.IBCTransferKeeper
	ibcCoreKeeper           vaastypes.IBCCoreKeeper
	feeCollectorName        string

	validatorAddressCodec addresscodec.Codec
	consensusAddressCodec addresscodec.Codec

	// Collections schema
	Schema collections.Schema

	// State collections
	Port                  collections.Item[string]
	ProviderClientID      collections.Item[string]
	ProviderChannelID     collections.Item[string]
	PendingChanges        collections.Item[vaastypes.ValidatorSetChangePacketData]
	InitGenesisHeight     collections.Item[uint64]
	PreVAAS               collections.Item[uint64]
	InitialValSet         collections.Item[types.GenesisState]
	Params                collections.Item[vaastypes.ConsumerParams]
	PrevStandaloneChain   collections.Item[[]byte]
	HeightValsetUpdateIDs collections.Map[uint64, uint64]
	CrossChainValidators  collections.Map[[]byte, types.CrossChainValidator]
	HistoricalInfos       collections.Map[int64, stakingtypes.HistoricalInfo]
}

// NewKeeper creates a new Consumer Keeper instance
// NOTE: the feeCollectorName is in reference to the consumer-chain fee
// collector (and not the provider chain)
func NewKeeper(
	cdc codec.BinaryCodec, storeService corestoretypes.KVStoreService,
	channelKeeper vaastypes.ChannelKeeper,
	connectionKeeper vaastypes.ConnectionKeeper, clientKeeper vaastypes.ClientKeeper,
	slashingKeeper vaastypes.SlashingKeeper, bankKeeper vaastypes.BankKeeper, accountKeeper vaastypes.AccountKeeper,
	ibcTransferKeeper vaastypes.IBCTransferKeeper, ibcCoreKeeper vaastypes.IBCCoreKeeper,
	feeCollectorName, authority string, validatorAddressCodec,
	consensusAddressCodec addresscodec.Codec,
) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		authority:               authority,
		storeService:            storeService,
		cdc:                     cdc,
		channelKeeper:           channelKeeper,
		connectionKeeper:        connectionKeeper,
		clientKeeper:            clientKeeper,
		slashingKeeper:          slashingKeeper,
		bankKeeper:              bankKeeper,
		authKeeper:              accountKeeper,
		ibcTransferKeeper:       ibcTransferKeeper,
		ibcCoreKeeper:           ibcCoreKeeper,
		feeCollectorName:        feeCollectorName,
		standaloneStakingKeeper: nil,
		validatorAddressCodec:   validatorAddressCodec,
		consensusAddressCodec:   consensusAddressCodec,

		// Initialize collections
		Port:                  collections.NewItem(sb, types.PortPrefix, "port", collections.StringValue),
		ProviderClientID:      collections.NewItem(sb, types.ProviderClientIDPrefix, "provider_client_id", collections.StringValue),
		ProviderChannelID:     collections.NewItem(sb, types.ProviderChannelIDPrefix, "provider_channel_id", collections.StringValue),
		PendingChanges:        collections.NewItem(sb, types.PendingChangesPrefix, "pending_changes", codec.CollValue[vaastypes.ValidatorSetChangePacketData](cdc)),
		InitGenesisHeight:     collections.NewItem(sb, types.InitGenesisHeightPrefix, "init_genesis_height", collections.Uint64Value),
		PreVAAS:               collections.NewItem(sb, types.PreVAASPrefix, "pre_vaas", collections.Uint64Value),
		InitialValSet:         collections.NewItem(sb, types.InitialValSetPrefix, "initial_val_set", codec.CollValue[types.GenesisState](cdc)),
		Params:                collections.NewItem(sb, types.ParametersPrefix, "params", codec.CollValue[vaastypes.ConsumerParams](cdc)),
		PrevStandaloneChain:   collections.NewItem(sb, types.PrevStandaloneChainPrefix, "prev_standalone_chain", collections.BytesValue),
		HeightValsetUpdateIDs: collections.NewMap(sb, types.HeightValsetUpdateIDPrefix, "height_valset_update_ids", collections.Uint64Key, collections.Uint64Value),
		CrossChainValidators:  collections.NewMap(sb, types.CrossChainValidatorPrefix, "cross_chain_validators", collections.BytesKey, codec.CollValue[types.CrossChainValidator](cdc)),
		HistoricalInfos:       collections.NewMap(sb, types.HistoricalInfoPrefix, "historical_infos", collections.Int64Key, codec.CollValue[stakingtypes.HistoricalInfo](cdc)),
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

// Returns a keeper with cdc and storeService set it does not raise any panics during registration (eg with IBCKeeper).
// Used only in testing.
func NewNonZeroKeeper(cdc codec.BinaryCodec, storeService corestoretypes.KVStoreService) Keeper {
	sb := collections.NewSchemaBuilder(storeService)

	k := Keeper{
		storeService: storeService,
		cdc:          cdc,

		// Initialize collections with minimal setup for testing
		Port:                  collections.NewItem(sb, types.PortPrefix, "port", collections.StringValue),
		ProviderClientID:      collections.NewItem(sb, types.ProviderClientIDPrefix, "provider_client_id", collections.StringValue),
		ProviderChannelID:     collections.NewItem(sb, types.ProviderChannelIDPrefix, "provider_channel_id", collections.StringValue),
		PendingChanges:        collections.NewItem(sb, types.PendingChangesPrefix, "pending_changes", codec.CollValue[vaastypes.ValidatorSetChangePacketData](cdc)),
		InitGenesisHeight:     collections.NewItem(sb, types.InitGenesisHeightPrefix, "init_genesis_height", collections.Uint64Value),
		PreVAAS:               collections.NewItem(sb, types.PreVAASPrefix, "pre_vaas", collections.Uint64Value),
		InitialValSet:         collections.NewItem(sb, types.InitialValSetPrefix, "initial_val_set", codec.CollValue[types.GenesisState](cdc)),
		Params:                collections.NewItem(sb, types.ParametersPrefix, "params", codec.CollValue[vaastypes.ConsumerParams](cdc)),
		PrevStandaloneChain:   collections.NewItem(sb, types.PrevStandaloneChainPrefix, "prev_standalone_chain", collections.BytesValue),
		HeightValsetUpdateIDs: collections.NewMap(sb, types.HeightValsetUpdateIDPrefix, "height_valset_update_ids", collections.Uint64Key, collections.Uint64Value),
		CrossChainValidators:  collections.NewMap(sb, types.CrossChainValidatorPrefix, "cross_chain_validators", collections.BytesKey, codec.CollValue[types.CrossChainValidator](cdc)),
		HistoricalInfos:       collections.NewMap(sb, types.HistoricalInfoPrefix, "historical_infos", collections.Int64Key, codec.CollValue[stakingtypes.HistoricalInfo](cdc)),
	}

	schema, err := sb.Build()
	if err != nil {
		panic(err)
	}
	k.Schema = schema

	return k
}

// SetStandaloneStakingKeeper sets the standalone staking keeper for the consumer chain.
// This method should only be called for previously standalone chains that are now consumers.
func (k *Keeper) SetStandaloneStakingKeeper(sk vaastypes.StakingKeeper) {
	k.standaloneStakingKeeper = sk
}

// ValidatorAddressCodec returns the app validator address codec.
func (k Keeper) ValidatorAddressCodec() addresscodec.Codec {
	return k.validatorAddressCodec
}

// ConsensusAddressCodec returns the app consensus address codec.
func (k Keeper) ConsensusAddressCodec() addresscodec.Codec {
	return k.consensusAddressCodec
}

// Logger returns a module-specific logger.
func (k Keeper) Logger(ctx sdk.Context) log.Logger {
	return ctx.Logger().With("module", "x/"+host.SubModuleName+"-"+types.ModuleName)
}

func (k *Keeper) SetHooks(sh vaastypes.ConsumerHooks) *Keeper {
	if k.hooks != nil {
		// This should never happen as SetHooks is expected
		// to be called only once in app.go
		panic("cannot set validator hooks twice")
	}

	k.hooks = sh

	return k
}

// ChanCloseInit defines a wrapper function for the channel Keeper's function
// Following ICS 004: https://github.com/cosmos/ibc/tree/main/spec/core/ics-004-channel-and-packet-semantics#closing-handshake
func (k Keeper) ChanCloseInit(ctx sdk.Context, portID, channelID string) error {
	return k.channelKeeper.ChanCloseInit(ctx, portID, channelID)
}

// ChannelOpenInit defines a wrapper function for the ibcCoreKeeper's function
func (k Keeper) ChannelOpenInit(ctx sdk.Context, msg *channeltypes.MsgChannelOpenInit) (
	*channeltypes.MsgChannelOpenInitResponse, error,
) {
	return k.ibcCoreKeeper.ChannelOpenInit(ctx, msg)
}

// GetPort returns the portID for the transfer module. Used in ExportGenesis
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

// SetProviderClientID sets the clientID for the client to the provider.
// Set in InitGenesis
func (k Keeper) SetProviderClientID(ctx context.Context, clientID string) {
	if err := k.ProviderClientID.Set(ctx, clientID); err != nil {
		panic(fmt.Errorf("failed to set provider client ID: %w", err))
	}
}

// GetProviderClientID gets the clientID for the client to the provider.
func (k Keeper) GetProviderClientID(ctx context.Context) (string, bool) {
	clientID, err := k.ProviderClientID.Get(ctx)
	if err != nil {
		return "", false
	}
	return clientID, true
}

// SetProviderChannel sets the channelID for the channel to the provider.
func (k Keeper) SetProviderChannel(ctx context.Context, channelID string) {
	if err := k.ProviderChannelID.Set(ctx, channelID); err != nil {
		panic(fmt.Errorf("failed to set provider channel: %w", err))
	}
}

// GetProviderChannel gets the channelID for the channel to the provider.
func (k Keeper) GetProviderChannel(ctx context.Context) (string, bool) {
	channelID, err := k.ProviderChannelID.Get(ctx)
	if err != nil {
		return "", false
	}
	if channelID == "" {
		return "", false
	}
	return channelID, true
}

// DeleteProviderChannel deletes the channelID for the channel to the provider.
func (k Keeper) DeleteProviderChannel(ctx context.Context) {
	if err := k.ProviderChannelID.Remove(ctx); err != nil {
		panic(fmt.Errorf("failed to delete provider channel: %w", err))
	}
}

// SetPendingChanges sets the pending validator set change packet that haven't been flushed to ABCI
func (k Keeper) SetPendingChanges(ctx context.Context, updates vaastypes.ValidatorSetChangePacketData) {
	if err := k.PendingChanges.Set(ctx, updates); err != nil {
		panic(fmt.Errorf("failed to set pending changes: %w", err))
	}
}

// GetPendingChanges gets the pending changes that haven't been flushed over ABCI
func (k Keeper) GetPendingChanges(ctx context.Context) (*vaastypes.ValidatorSetChangePacketData, bool) {
	data, err := k.PendingChanges.Get(ctx)
	if err != nil {
		return nil, false
	}
	return &data, true
}

// DeletePendingChanges deletes the pending changes after they've been flushed to ABCI
func (k Keeper) DeletePendingChanges(ctx context.Context) {
	if err := k.PendingChanges.Remove(ctx); err != nil {
		panic(fmt.Errorf("failed to delete pending changes: %w", err))
	}
}

func (k Keeper) GetInitGenesisHeight(ctx context.Context) int64 {
	height, err := k.InitGenesisHeight.Get(ctx)
	if err != nil {
		panic("last standalone height not set")
	}
	return int64(height)
}

func (k Keeper) SetInitGenesisHeight(ctx context.Context, height int64) {
	if err := k.InitGenesisHeight.Set(ctx, uint64(height)); err != nil {
		panic(fmt.Errorf("failed to set init genesis height: %w", err))
	}
}

func (k Keeper) IsPreVAAS(ctx context.Context) bool {
	has, err := k.PreVAAS.Has(ctx)
	if err != nil {
		return false
	}
	return has
}

func (k Keeper) SetPreVAASTrue(ctx context.Context) {
	if err := k.PreVAAS.Set(ctx, 1); err != nil {
		panic(fmt.Errorf("failed to set pre-VAAS: %w", err))
	}
}

func (k Keeper) DeletePreVAAS(ctx context.Context) {
	if err := k.PreVAAS.Remove(ctx); err != nil {
		panic(fmt.Errorf("failed to delete pre-VAAS: %w", err))
	}
}

func (k Keeper) SetInitialValSet(ctx context.Context, initialValSet []tmtypes.ValidatorUpdate) {
	// Store the initial validator set in a GenesisState wrapper
	initialValSetState := types.GenesisState{
		Provider: vaastypes.ProviderInfo{InitialValSet: initialValSet},
	}
	if err := k.InitialValSet.Set(ctx, initialValSetState); err != nil {
		panic(fmt.Errorf("failed to set initial val set: %w", err))
	}
}

func (k Keeper) GetInitialValSet(ctx context.Context) []tmtypes.ValidatorUpdate {
	initialValSetState, err := k.InitialValSet.Get(ctx)
	if err != nil {
		return []tmtypes.ValidatorUpdate{}
	}
	return initialValSetState.Provider.InitialValSet
}

func (k Keeper) GetLastStandaloneValidators(ctx sdk.Context) ([]stakingtypes.Validator, error) {
	if !k.IsPreVAAS(ctx) || k.standaloneStakingKeeper == nil {
		panic("cannot get last standalone validators if not in pre-VAAS state, or if standalone staking keeper is nil")
	}
	return k.GetLastBondedValidators(ctx)
}

// VerifyProviderChain verifies that the chain trying to connect on the channel handshake
// is the expected provider chain.
func (k Keeper) VerifyProviderChain(ctx sdk.Context, connectionHops []string) error {
	if len(connectionHops) != 1 {
		return errorsmod.Wrap(channeltypes.ErrTooManyConnectionHops, "must have direct connection to provider chain")
	}
	connectionID := connectionHops[0]
	conn, ok := k.connectionKeeper.GetConnection(ctx, connectionID)
	if !ok {
		return errorsmod.Wrapf(conntypes.ErrConnectionNotFound, "connection not found for connection ID: %s", connectionID)
	}
	// Verify that client id is expected clientID
	expectedClientId, ok := k.GetProviderClientID(ctx)
	if !ok {
		return errorsmod.Wrapf(clienttypes.ErrInvalidClient, "could not find provider client id")
	}
	if expectedClientId != conn.ClientId {
		return errorsmod.Wrapf(clienttypes.ErrInvalidClient, "invalid client: %s, channel must be built on top of client: %s", conn.ClientId, expectedClientId)
	}

	return nil
}

// SetHeightValsetUpdateID sets the valset update id for a given block height
func (k Keeper) SetHeightValsetUpdateID(ctx context.Context, height, valsetUpdateId uint64) {
	if err := k.HeightValsetUpdateIDs.Set(ctx, height, valsetUpdateId); err != nil {
		panic(fmt.Errorf("failed to set height valset update ID: %w", err))
	}
}

// GetHeightValsetUpdateID gets the valset update id recorded for a given block height
func (k Keeper) GetHeightValsetUpdateID(ctx context.Context, height uint64) uint64 {
	valsetUpdateId, err := k.HeightValsetUpdateIDs.Get(ctx, height)
	if err != nil {
		return 0
	}
	return valsetUpdateId
}

// DeleteHeightValsetUpdateID deletes the valset update id for a given block height
func (k Keeper) DeleteHeightValsetUpdateID(ctx context.Context, height uint64) {
	if err := k.HeightValsetUpdateIDs.Remove(ctx, height); err != nil {
		panic(fmt.Errorf("failed to delete height valset update ID: %w", err))
	}
}

// GetAllHeightToValsetUpdateIDs returns a list of all the block heights to valset update IDs in the store
//
// Note that the block height to vscID mapping is stored under keys with the following format:
// HeightValsetUpdateIDKeyPrefix | height
// Thus, the returned array is in ascending order of heights.
func (k Keeper) GetAllHeightToValsetUpdateIDs(ctx context.Context) (heightToValsetUpdateIDs []types.HeightToValsetUpdateID) {
	iter, err := k.HeightValsetUpdateIDs.Iterate(ctx, nil)
	if err != nil {
		return heightToValsetUpdateIDs
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		kv, err := iter.KeyValue()
		if err != nil {
			continue
		}
		heightToValsetUpdateIDs = append(heightToValsetUpdateIDs, types.HeightToValsetUpdateID{
			Height:         kv.Key,
			ValsetUpdateId: kv.Value,
		})
	}

	return heightToValsetUpdateIDs
}

// SetCCValidator sets a cross-chain validator under its validator address
func (k Keeper) SetCCValidator(ctx context.Context, v types.CrossChainValidator) {
	if err := k.CrossChainValidators.Set(ctx, v.Address, v); err != nil {
		panic(fmt.Errorf("failed to set cross-chain validator: %w", err))
	}
}

// GetCCValidator returns a cross-chain validator for a given address
func (k Keeper) GetCCValidator(ctx context.Context, addr []byte) (validator types.CrossChainValidator, found bool) {
	val, err := k.CrossChainValidators.Get(ctx, addr)
	if err != nil {
		return types.CrossChainValidator{}, false
	}
	return val, true
}

// DeleteCCValidator deletes a cross-chain validator for a given address
func (k Keeper) DeleteCCValidator(ctx context.Context, addr []byte) {
	if err := k.CrossChainValidators.Remove(ctx, addr); err != nil {
		panic(fmt.Errorf("failed to delete cross-chain validator: %w", err))
	}
}

// GetAllCCValidator returns all cross-chain validators
//
// Note that the cross-chain validators are stored under keys with the following format:
// CrossChainValidatorKeyPrefix | address
// Thus, the returned array is in ascending order of addresses.
func (k Keeper) GetAllCCValidator(ctx context.Context) (validators []types.CrossChainValidator) {
	iter, err := k.CrossChainValidators.Iterate(ctx, nil)
	if err != nil {
		return validators
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		val, err := iter.Value()
		if err != nil {
			continue
		}
		validators = append(validators, val)
	}

	return validators
}

func (k Keeper) MarkAsPrevStandaloneChain(ctx context.Context) {
	if err := k.PrevStandaloneChain.Set(ctx, []byte{}); err != nil {
		panic(fmt.Errorf("failed to mark as prev standalone chain: %w", err))
	}
}

func (k Keeper) IsPrevStandaloneChain(ctx context.Context) bool {
	has, err := k.PrevStandaloneChain.Has(ctx)
	if err != nil {
		return false
	}
	return has
}

// GetLastBondedValidators iterates the last validator powers in the staking module
// and returns the first MaxValidators many validators with the largest powers.
func (k Keeper) GetLastBondedValidators(ctx sdk.Context) ([]stakingtypes.Validator, error) {
	maxVals, err := k.standaloneStakingKeeper.MaxValidators(ctx)
	if err != nil {
		return nil, err
	}
	return vaastypes.GetLastBondedValidatorsUtil(ctx, k.standaloneStakingKeeper, maxVals)
}
