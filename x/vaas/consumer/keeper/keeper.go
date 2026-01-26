package keeper

import (
	"encoding/binary"
	"fmt"
	"reflect"

	"github.com/allinbits/vaas/x/vaas/consumer/types"
	vaastypes "github.com/allinbits/vaas/x/vaas/types"

	tmtypes "github.com/cometbft/cometbft/abci/types"

	clienttypes "github.com/cosmos/ibc-go/v10/modules/core/02-client/types"
	conntypes "github.com/cosmos/ibc-go/v10/modules/core/03-connection/types"
	channeltypes "github.com/cosmos/ibc-go/v10/modules/core/04-channel/types"
	host "github.com/cosmos/ibc-go/v10/modules/core/24-host"

	addresscodec "cosmossdk.io/core/address"
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"

	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
)

// Keeper defines the Cross-Chain Validation Consumer Keeper
type Keeper struct {
	// the address capable of executing a MsgUpdateParams message. Typically, this
	// should be the x/gov module account.
	authority string

	storeKey         storetypes.StoreKey
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
}

// NewKeeper creates a new Consumer Keeper instance
// NOTE: the feeCollectorName is in reference to the consumer-chain fee
// collector (and not the provider chain)
func NewKeeper(
	cdc codec.BinaryCodec, key storetypes.StoreKey,
	channelKeeper vaastypes.ChannelKeeper,
	connectionKeeper vaastypes.ConnectionKeeper, clientKeeper vaastypes.ClientKeeper,
	slashingKeeper vaastypes.SlashingKeeper, bankKeeper vaastypes.BankKeeper, accountKeeper vaastypes.AccountKeeper,
	ibcTransferKeeper vaastypes.IBCTransferKeeper, ibcCoreKeeper vaastypes.IBCCoreKeeper,
	feeCollectorName, authority string, validatorAddressCodec,
	consensusAddressCodec addresscodec.Codec,
) Keeper {
	k := Keeper{
		authority:               authority,
		storeKey:                key,
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
	}

	k.mustValidateFields()
	return k
}

// GetAuthority returns the x/ccv/provider module's authority.
func (k Keeper) GetAuthority() string {
	return k.authority
}

// Returns a keeper with cdc and key set it does not raise any panics during registration (eg with IBCKeeper).
// Used only in testing.
func NewNonZeroKeeper(cdc codec.BinaryCodec, key storetypes.StoreKey) Keeper {
	return Keeper{
		storeKey: key,
		cdc:      cdc,
	}
}

// SetStandaloneStakingKeeper sets the standalone staking keeper for the consumer chain.
// This method should only be called for previously standalone chains that are now consumers.
func (k *Keeper) SetStandaloneStakingKeeper(sk vaastypes.StakingKeeper) {
	k.standaloneStakingKeeper = sk
}

// Validates that the consumer keeper is initialized with non-zero and
// non-nil values for all its fields. Otherwise this method will panic.
func (k Keeper) mustValidateFields() {
	// Ensures no fields are missed in this validation
	if reflect.ValueOf(k).NumField() != 16 {
		panic("number of fields in consumer keeper is not 16")
	}

	// Note 116 / 16 fields will be validated,
	// hooks are explicitly set after the constructor,
	// stakingKeeper is optionally set after the constructor,

	vaastypes.PanicIfZeroOrNil(k.storeKey, "storeKey")                           // 1
	vaastypes.PanicIfZeroOrNil(k.cdc, "cdc")                                     // 2
	vaastypes.PanicIfZeroOrNil(k.channelKeeper, "channelKeeper")                 // 4
	vaastypes.PanicIfZeroOrNil(k.connectionKeeper, "connectionKeeper")           // 6
	vaastypes.PanicIfZeroOrNil(k.clientKeeper, "clientKeeper")                   // 7
	vaastypes.PanicIfZeroOrNil(k.slashingKeeper, "slashingKeeper")               // 8
	vaastypes.PanicIfZeroOrNil(k.bankKeeper, "bankKeeper")                       // 9
	vaastypes.PanicIfZeroOrNil(k.authKeeper, "authKeeper")                       // 10
	vaastypes.PanicIfZeroOrNil(k.ibcTransferKeeper, "ibcTransferKeeper")         // 11
	vaastypes.PanicIfZeroOrNil(k.ibcCoreKeeper, "ibcCoreKeeper")                 // 12
	vaastypes.PanicIfZeroOrNil(k.feeCollectorName, "feeCollectorName")           // 13
	vaastypes.PanicIfZeroOrNil(k.authority, "authority")                         // 14
	vaastypes.PanicIfZeroOrNil(k.validatorAddressCodec, "validatorAddressCodec") // 15
	vaastypes.PanicIfZeroOrNil(k.consensusAddressCodec, "consensusAddressCodec") // 16
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
func (k Keeper) GetPort(ctx sdk.Context) string {
	store := ctx.KVStore(k.storeKey)
	return string(store.Get(types.PortKey()))
}

// SetPort sets the portID for the CCV module. Used in InitGenesis
func (k Keeper) SetPort(ctx sdk.Context, portID string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.PortKey(), []byte(portID))
}

// SetProviderClientID sets the clientID for the client to the provider.
// Set in InitGenesis
func (k Keeper) SetProviderClientID(ctx sdk.Context, clientID string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ProviderClientIDKey(), []byte(clientID))
}

// GetProviderClientID gets the clientID for the client to the provider.
func (k Keeper) GetProviderClientID(ctx sdk.Context) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	clientIdBytes := store.Get(types.ProviderClientIDKey())
	if clientIdBytes == nil {
		return "", false
	}
	return string(clientIdBytes), true
}

// SetProviderChannel sets the channelID for the channel to the provider.
func (k Keeper) SetProviderChannel(ctx sdk.Context, channelID string) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.ProviderChannelIDKey(), []byte(channelID))
}

// GetProviderChannel gets the channelID for the channel to the provider.
func (k Keeper) GetProviderChannel(ctx sdk.Context) (string, bool) {
	store := ctx.KVStore(k.storeKey)
	channelIdBytes := store.Get(types.ProviderChannelIDKey())
	if len(channelIdBytes) == 0 {
		return "", false
	}
	return string(channelIdBytes), true
}

// DeleteProviderChannel deletes the channelID for the channel to the provider.
func (k Keeper) DeleteProviderChannel(ctx sdk.Context) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.ProviderChannelIDKey())
}

// SetPendingChanges sets the pending validator set change packet that haven't been flushed to ABCI
func (k Keeper) SetPendingChanges(ctx sdk.Context, updates vaastypes.ValidatorSetChangePacketData) {
	store := ctx.KVStore(k.storeKey)
	bz, err := updates.Marshal()
	if err != nil {
		// This should never happen
		panic(fmt.Errorf("failed to marshal PendingChanges: %w", err))
	}
	store.Set(types.PendingChangesKey(), bz)
}

// GetPendingChanges gets the pending changes that haven't been flushed over ABCI
func (k Keeper) GetPendingChanges(ctx sdk.Context) (*vaastypes.ValidatorSetChangePacketData, bool) {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.PendingChangesKey())
	if bz == nil {
		return nil, false
	}
	var data vaastypes.ValidatorSetChangePacketData
	if err := data.Unmarshal(bz); err != nil {
		// This should never happen as PendingChanges is expected
		// to be correctly serialized in SetPendingChanges
		panic(fmt.Errorf("failed to unmarshal PendingChanges: %w", err))
	}
	return &data, true
}

// DeletePendingChanges deletes the pending changes after they've been flushed to ABCI
func (k Keeper) DeletePendingChanges(ctx sdk.Context) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.PendingChangesKey())
}

func (k Keeper) GetInitGenesisHeight(ctx sdk.Context) int64 {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.InitGenesisHeightKey())
	if bz == nil {
		panic("last standalone height not set")
	}
	height := sdk.BigEndianToUint64(bz)
	return int64(height)
}

func (k Keeper) SetInitGenesisHeight(ctx sdk.Context, height int64) {
	bz := sdk.Uint64ToBigEndian(uint64(height))
	store := ctx.KVStore(k.storeKey)
	store.Set(types.InitGenesisHeightKey(), bz)
}

func (k Keeper) IsPreVAAS(ctx sdk.Context) bool {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.PreVAASKey())
	return bz != nil
}

func (k Keeper) SetPreVAASTrue(ctx sdk.Context) {
	store := ctx.KVStore(k.storeKey)
	bz := sdk.Uint64ToBigEndian(uint64(1))
	store.Set(types.PreVAASKey(), bz)
}

func (k Keeper) DeletePreVAAS(ctx sdk.Context) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.PreVAASKey())
}

func (k Keeper) SetInitialValSet(ctx sdk.Context, initialValSet []tmtypes.ValidatorUpdate) {
	store := ctx.KVStore(k.storeKey)
	// TODO it's not necessary to store the entire genesis state
	initialValSetState := types.GenesisState{
		Provider: vaastypes.ProviderInfo{InitialValSet: initialValSet},
	}
	bz := k.cdc.MustMarshal(&initialValSetState)
	store.Set(types.InitialValSetKey(), bz)
}

func (k Keeper) GetInitialValSet(ctx sdk.Context) []tmtypes.ValidatorUpdate {
	store := ctx.KVStore(k.storeKey)
	initialValSet := types.GenesisState{}
	bz := store.Get(types.InitialValSetKey())
	if bz != nil {
		k.cdc.MustUnmarshal(bz, &initialValSet)
		return initialValSet.Provider.InitialValSet
	}
	return []tmtypes.ValidatorUpdate{}
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
func (k Keeper) SetHeightValsetUpdateID(ctx sdk.Context, height, valsetUpdateId uint64) {
	store := ctx.KVStore(k.storeKey)
	valBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(valBytes, valsetUpdateId)
	store.Set(types.HeightValsetUpdateIDKey(height), valBytes)
}

// GetHeightValsetUpdateID gets the valset update id recorded for a given block height
func (k Keeper) GetHeightValsetUpdateID(ctx sdk.Context, height uint64) uint64 {
	store := ctx.KVStore(k.storeKey)
	bz := store.Get(types.HeightValsetUpdateIDKey(height))
	if bz == nil {
		return 0
	}
	return binary.BigEndian.Uint64(bz)
}

// DeleteHeightValsetUpdateID deletes the valset update id for a given block height
func (k Keeper) DeleteHeightValsetUpdateID(ctx sdk.Context, height uint64) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.HeightValsetUpdateIDKey(height))
}

// GetAllHeightToValsetUpdateIDs returns a list of all the block heights to valset update IDs in the store
//
// Note that the block height to vscID mapping is stored under keys with the following format:
// HeightValsetUpdateIDKeyPrefix | height
// Thus, the returned array is in ascending order of heights.
func (k Keeper) GetAllHeightToValsetUpdateIDs(ctx sdk.Context) (heightToValsetUpdateIDs []types.HeightToValsetUpdateID) {
	store := ctx.KVStore(k.storeKey)
	iterator := storetypes.KVStorePrefixIterator(store, types.HeightValsetUpdateIDKeyPrefix())

	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		height := binary.BigEndian.Uint64(iterator.Key()[1:])
		vscID := binary.BigEndian.Uint64(iterator.Value())

		heightToValsetUpdateIDs = append(heightToValsetUpdateIDs, types.HeightToValsetUpdateID{
			Height:         height,
			ValsetUpdateId: vscID,
		})
	}

	return heightToValsetUpdateIDs
}

// SetCCValidator sets a cross-chain validator under its validator address
func (k Keeper) SetCCValidator(ctx sdk.Context, v types.CrossChainValidator) {
	store := ctx.KVStore(k.storeKey)
	bz := k.cdc.MustMarshal(&v)

	store.Set(types.CrossChainValidatorKey(v.Address), bz)
}

// GetCCValidator returns a cross-chain validator for a given address
func (k Keeper) GetCCValidator(ctx sdk.Context, addr []byte) (validator types.CrossChainValidator, found bool) {
	store := ctx.KVStore(k.storeKey)
	v := store.Get(types.CrossChainValidatorKey(addr))
	if v == nil {
		return
	}
	k.cdc.MustUnmarshal(v, &validator)
	found = true

	return
}

// DeleteCCValidator deletes a cross-chain validator for a given address
func (k Keeper) DeleteCCValidator(ctx sdk.Context, addr []byte) {
	store := ctx.KVStore(k.storeKey)
	store.Delete(types.CrossChainValidatorKey(addr))
}

// GetAllCCValidator returns all cross-chain validators
//
// Note that the cross-chain validators are stored under keys with the following format:
// CrossChainValidatorKeyPrefix | address
// Thus, the returned array is in ascending order of addresses.
func (k Keeper) GetAllCCValidator(ctx sdk.Context) (validators []types.CrossChainValidator) {
	store := ctx.KVStore(k.storeKey)
	iterator := storetypes.KVStorePrefixIterator(store, types.CrossChainValidatorKeyPrefix())

	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		val := types.CrossChainValidator{}
		k.cdc.MustUnmarshal(iterator.Value(), &val)
		validators = append(validators, val)
	}

	return validators
}

func (k Keeper) MarkAsPrevStandaloneChain(ctx sdk.Context) {
	store := ctx.KVStore(k.storeKey)
	store.Set(types.PrevStandaloneChainKey(), []byte{})
}

func (k Keeper) IsPrevStandaloneChain(ctx sdk.Context) bool {
	store := ctx.KVStore(k.storeKey)
	return store.Has(types.PrevStandaloneChainKey())
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
